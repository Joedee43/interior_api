package apidocs_test

import (
	"bufio"
	"bytes"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Joedee43/interior_api/internal/apidocs"
	"github.com/Joedee43/interior_api/internal/apitest"
)

var (
	pathLine        = regexp.MustCompile(`^  (/\S*):\s*$`)
	methodLine      = regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	operationIDLine = regexp.MustCompile(`^      operationId: ([A-Za-z][A-Za-z0-9]*)\s*$`)
	braces          = regexp.MustCompile(`\{(\w+)\}`)
)

// specOperations reads "METHOD /path" pairs from the YAML by indentation
// (paths at 2 spaces, methods at 4) — enough for this file's fixed layout
// without a YAML dependency. {param} becomes :param to match Gin.
func specOperations(t *testing.T) map[string]bool {
	t.Helper()
	ops := map[string]bool{}
	current := ""
	sc := bufio.NewScanner(bytes.NewReader(apidocs.Spec()))
	for sc.Scan() {
		line := sc.Text()
		if m := pathLine.FindStringSubmatch(line); m != nil {
			current = braces.ReplaceAllString(m[1], ":$1")
			continue
		}
		if m := methodLine.FindStringSubmatch(line); m != nil && current != "" {
			ops[strings.ToUpper(m[1])+" "+current] = true
		}
	}
	return ops
}

// specOperationIDs returns each documented route's generator-safe name.
// Keeping this small parser beside the route drift parser avoids adding a
// runtime YAML dependency only for a contract test.
func specOperationIDs(t *testing.T) map[string]string {
	t.Helper()
	ids := map[string]string{}
	currentPath, currentOperation := "", ""
	sc := bufio.NewScanner(bytes.NewReader(apidocs.Spec()))
	for sc.Scan() {
		line := sc.Text()
		if m := pathLine.FindStringSubmatch(line); m != nil {
			currentPath = braces.ReplaceAllString(m[1], ":$1")
			currentOperation = ""
			continue
		}
		if m := methodLine.FindStringSubmatch(line); m != nil && currentPath != "" {
			currentOperation = strings.ToUpper(m[1]) + " " + currentPath
			ids[currentOperation] = ""
			continue
		}
		if m := operationIDLine.FindStringSubmatch(line); m != nil && currentOperation != "" {
			ids[currentOperation] = m[1]
			currentOperation = ""
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan OpenAPI operationIds: %v", err)
	}
	return ids
}

func TestSpecMatchesRoutes(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	engine, ok := e.Router.(*gin.Engine)
	if !ok {
		t.Fatal("router is not a *gin.Engine")
	}
	routes := map[string]bool{}
	for _, r := range engine.Routes() {
		routes[r.Method+" "+r.Path] = true
	}
	spec := specOperations(t)

	var missing, extra []string
	for op := range routes {
		if !spec[op] {
			missing = append(missing, op)
		}
	}
	for op := range spec {
		if !routes[op] {
			extra = append(extra, op)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	for _, op := range missing {
		t.Errorf("route not documented in openapi.yaml: %s", op)
	}
	for _, op := range extra {
		t.Errorf("openapi.yaml documents a route that does not exist: %s", op)
	}
}

func TestSpecIsServed(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	w := e.Do(http.MethodGet, "/openapi.yaml", "")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "openapi: 3.1.0") {
		t.Errorf("GET /openapi.yaml: %d", w.Code)
	}
}

func TestEveryOperationHasUniqueOperationID(t *testing.T) {
	ids := specOperationIDs(t)
	seen := map[string]string{}
	for operation, id := range ids {
		if id == "" {
			t.Errorf("%s has no operationId", operation)
			continue
		}
		if previous, exists := seen[id]; exists {
			t.Errorf("operationId %q is shared by %s and %s", id, previous, operation)
		}
		seen[id] = operation
	}
}
