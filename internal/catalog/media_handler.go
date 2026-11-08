package catalog

import (
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Joedee43/interior_api/internal/httpapi"
)

// uploadDeadline replaces the server-wide read/write timeouts (15s/30s)
// for upload requests only, so a 50 MB model on a slow connection is not
// cut off mid-transfer while ordinary requests keep tight timeouts.
const uploadDeadline = 5 * time.Minute

// multipartOverhead allows for multipart boundaries and small text fields
// on top of the file itself when capping the request body.
const multipartOverhead = 1 << 20

func (h *Handler) mountMedia(p *gin.RouterGroup, companyGroup *gin.RouterGroup, public *gin.RouterGroup) {
	p.GET("/:productID/media", h.listMedia)
	p.POST("/:productID/images", httpapi.LimitBody(MaxImageBytes+multipartOverhead), h.uploadImage)
	p.PATCH("/:productID/images/:mediaID", h.updateImage)
	p.DELETE("/:productID/images/:mediaID", h.deleteImage)
	p.PUT("/:productID/model", httpapi.LimitBody(MaxModelBytes+multipartOverhead), h.uploadModel)
	p.DELETE("/:productID/model", h.deleteModel)

	companyGroup.GET("/media/:mediaID/file", h.companyFile)
	public.GET("/:mediaID", h.publicFile)
}

// ---- DTOs -------------------------------------------------------------

func publicMediaURL(id string) string { return "/v1/media/" + id }

type mediaDTO struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Width       *int32 `json:"width,omitempty"`
	Height      *int32 `json:"height,omitempty"`
	AltText     string `json:"alt_text,omitempty"`
	Position    int    `json:"position"`
	// The next three are always present: omitempty would drop is_primary
	// when false and the arrays when empty, and the frontend requires them.
	IsPrimary bool     `json:"is_primary"`
	Materials []string `json:"materials"`
	Variants  []string `json:"variants"`
}

// companyMediaDTO adds member-only fields: the private download URL (works
// for drafts too), the original file name and the checksum.
type companyMediaDTO struct {
	mediaDTO
	FileURL          string    `json:"file_url"`
	OriginalFilename string    `json:"original_filename"`
	SHA256           string    `json:"sha256"`
	CreatedAt        time.Time `json:"created_at"`
}

func toMediaDTO(m Media) mediaDTO {
	return mediaDTO{
		ID: m.ID, Kind: m.Kind, URL: publicMediaURL(m.ID), ContentType: m.ContentType, SizeBytes: m.SizeBytes,
		Width: m.Width, Height: m.Height, AltText: m.AltText, Position: m.Position, IsPrimary: m.IsPrimary,
		Materials: nonNil(m.Materials), Variants: nonNil(m.Variants),
	}
}

// nonNil turns a nil slice into an empty one, so JSON gets [] instead of null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toCompanyMediaDTO(m Media) companyMediaDTO {
	return companyMediaDTO{
		mediaDTO:         toMediaDTO(m),
		FileURL:          "/v1/companies/" + m.CompanyID + "/media/" + m.ID + "/file",
		OriginalFilename: m.OriginalFilename,
		SHA256:           hex.EncodeToString(m.SHA256),
		CreatedAt:        m.CreatedAt,
	}
}

type materialCheckDTO struct {
	ModelPresent        bool     `json:"model_present"`
	MissingMaterialKeys []string `json:"missing_material_keys"`
}

// ---- handlers ---------------------------------------------------------

func (h *Handler) listMedia(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound}) {
		return
	}
	pm, mc, err := h.svc.Media(c.Request.Context(), member(c), c.Param("productID"))
	if err != nil {
		writeError(c, err)
		return
	}
	images := make([]companyMediaDTO, 0, len(pm.Images))
	for _, im := range pm.Images {
		images = append(images, toCompanyMediaDTO(im))
	}
	var model *companyMediaDTO
	if pm.Model != nil {
		d := toCompanyMediaDTO(*pm.Model)
		model = &d
	}
	c.JSON(http.StatusOK, gin.H{
		"images":         images,
		"model":          model,
		"material_check": materialCheckDTO{ModelPresent: mc.ModelPresent, MissingMaterialKeys: mc.Missing},
	})
}

// upload holds what readUpload found in the multipart body.
type upload struct {
	staged   *Staged
	filename string
	altText  string
}

// readUpload streams a multipart/form-data body part by part: the file
// part goes straight to Stage (into storage), small text fields are read
// into memory with a cap. Fields may come before or after the file.
func (h *Handler) readUpload(c *gin.Context, kind string, allowAlt bool) (upload, error) {
	var up upload
	mr, err := c.Request.MultipartReader()
	if err != nil {
		return up, badUpload("send multipart/form-data with a \"file\" field")
	}
	ctx := c.Request.Context()
	fail := func(err error) (upload, error) {
		h.svc.Discard(ctx, up.staged) // undo a file staged earlier in this body
		return upload{}, err
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return fail(ErrFileTooLarge)
			}
			return fail(badUpload("malformed multipart body"))
		}
		switch part.FormName() {
		case "file":
			if up.staged != nil {
				return fail(badUpload("send exactly one \"file\" field"))
			}
			up.filename = part.FileName()
			if up.staged, err = h.svc.Stage(ctx, kind, part); err != nil {
				return fail(err)
			}
		case "alt_text":
			if !allowAlt {
				return fail(badUpload("unexpected field \"alt_text\""))
			}
			b, err := readField(part, 2048)
			if err != nil {
				return fail(err)
			}
			up.altText = b
		default:
			return fail(badUpload("unexpected field \"" + part.FormName() + "\""))
		}
	}
	if up.staged == nil {
		return upload{}, badUpload("missing \"file\" field")
	}
	return up, nil
}

func readField(part *multipart.Part, max int64) (string, error) {
	b, err := io.ReadAll(io.LimitReader(part, max+1))
	if err != nil {
		return "", badUpload("could not read form field")
	}
	if int64(len(b)) > max {
		return "", badUpload("form field \"" + part.FormName() + "\" is too long")
	}
	return string(b), nil
}

// badUpload reports a malformed request as a validation error on "file".
func badUpload(msg string) error {
	return &FileProblem{Reason: msg}
}

// extendDeadlines lifts the server timeouts for this request only. It is
// best effort: writers that do not support deadlines (httptest) ignore it.
func extendDeadlines(c *gin.Context) {
	rc := http.NewResponseController(c.Writer)
	_ = rc.SetReadDeadline(time.Now().Add(uploadDeadline))
	_ = rc.SetWriteDeadline(time.Now().Add(uploadDeadline))
}

func (h *Handler) uploadImage(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound}) {
		return
	}
	// Check the product before reading a possibly large body.
	if _, err := h.svc.Get(c.Request.Context(), member(c), c.Param("productID")); err != nil {
		writeError(c, err)
		return
	}
	extendDeadlines(c)
	up, err := h.readUpload(c, MediaImage, true)
	if err != nil {
		writeError(c, err)
		return
	}
	m, err := h.svc.AttachImage(c.Request.Context(), member(c), c.Param("productID"), up.staged, up.altText, up.filename)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toCompanyMediaDTO(m))
}

func (h *Handler) uploadModel(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound}) {
		return
	}
	if _, err := h.svc.Get(c.Request.Context(), member(c), c.Param("productID")); err != nil {
		writeError(c, err)
		return
	}
	extendDeadlines(c)
	up, err := h.readUpload(c, MediaModel, false)
	if err != nil {
		writeError(c, err)
		return
	}
	m, err := h.svc.ReplaceModel(c.Request.Context(), member(c), c.Param("productID"), up.staged, up.filename)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toCompanyMediaDTO(m))
}

type updateImageRequest struct {
	AltText   *string `json:"alt_text"`
	Position  *int    `json:"position"`
	IsPrimary *bool   `json:"is_primary"`
}

func (h *Handler) updateImage(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound, "mediaID": ErrMediaNotFound}) {
		return
	}
	var req updateImageRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	m, err := h.svc.UpdateImage(c.Request.Context(), member(c), c.Param("productID"), c.Param("mediaID"), ImageUpdate(req))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toCompanyMediaDTO(m))
}

func (h *Handler) deleteImage(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound, "mediaID": ErrMediaNotFound}) {
		return
	}
	if err := h.svc.DeleteImage(c.Request.Context(), member(c), c.Param("productID"), c.Param("mediaID")); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) deleteModel(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound}) {
		return
	}
	if err := h.svc.DeleteModel(c.Request.Context(), member(c), c.Param("productID")); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) publicFile(c *gin.Context) {
	if !uuidParams(c, map[string]error{"mediaID": ErrMediaNotFound}) {
		return
	}
	m, f, err := h.svc.OpenPublicFile(c.Request.Context(), c.Param("mediaID"))
	if err != nil {
		writeError(c, err)
		return
	}
	// A media id always refers to the same bytes (a new upload gets a new
	// id), so browsers and CDNs may cache it for a year without revalidating.
	serveFile(c, m, f, "public, max-age=31536000, immutable")
}

func (h *Handler) companyFile(c *gin.Context) {
	if !uuidParams(c, map[string]error{"mediaID": ErrMediaNotFound}) {
		return
	}
	m, f, err := h.svc.OpenCompanyFile(c.Request.Context(), member(c), c.Param("mediaID"))
	if err != nil {
		writeError(c, err)
		return
	}
	// May be a draft's file: never store it in shared caches.
	serveFile(c, m, f, "private, no-store")
}

// serveFile streams a stored file. http.ServeContent handles Range
// requests (206 partial content), If-None-Match against the ETag and
// HEAD; it reads from f lazily, so large models are never held in memory.
func serveFile(c *gin.Context, m Media, f io.ReadSeekCloser, cacheControl string) {
	defer f.Close()
	h := c.Writer.Header()
	h.Set("Content-Type", m.ContentType) // set explicitly: ServeContent will not guess
	h.Set("ETag", `"`+hex.EncodeToString(m.SHA256)+`"`)
	h.Set("Cache-Control", cacheControl)
	// The browser must use our Content-Type and never execute the file as
	// a page or script, whatever bytes it contains.
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	http.ServeContent(c.Writer, c.Request, "", m.CreatedAt, f)
}
