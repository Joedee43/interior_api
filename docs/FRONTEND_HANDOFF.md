# Frontend handoff — Next.js

Contract for the sibling `interior_web` repository (Next.js App Router,
TypeScript, React Three Fiber/Drei and Three.js). The OpenAPI source of truth
is `GET /openapi.yaml`; generated clients use its unique `operationId`s.

## Topology, cookies and client IPs

The normal deployment is one browser origin. Next.js rewrites `/v1/*` and
`/openapi.yaml` to Go in development and production. Browser code always uses
relative URLs and `credentials: "include"`; it never sees the internal Go URL.

- Production session cookie: `__Host-interior_session` (HttpOnly, Secure,
  SameSite=Lax, Path=/). Development uses `interior_session` without Secure.
- `ALLOWED_ORIGINS` must include the frontend origin. A forwarded browser
  POST/PUT/PATCH/DELETE keeps its browser `Origin`, and Go's CSRF check requires
  that exact origin.
- Next.js Server Component/server-action requests normally send no `Origin`.
  `checkOrigin` deliberately permits an absent Origin for server-to-server
  clients that cannot carry a victim browser's ambient cookie.
- Next.js must forward the incoming session cookie on authenticated server
  fetches and return every `Set-Cookie` header from auth responses unchanged.
- Rewrites supply proxy headers. The shared server-fetch helper must also
  resolve the client address from trusted platform request context, then
  overwrite both `X-Forwarded-For` and `X-Real-IP`. Never append an arbitrary
  browser-supplied forwarding chain.
- Go's `TRUSTED_PROXIES` contains only the Next.js server/private proxy CIDRs.
  Forwarding headers from any other peer are ignored.

`ENABLE_CORS=true` is an optional alternative for a deliberate separate
same-site frontend/API deployment. It allows credentials only from exact
`ALLOWED_ORIGINS`; never combine credentialed CORS with `*`.

## Requests, errors and pagination

JSON request bodies are strict and limited to 1 MiB. Upload endpoints use
multipart form data and their documented larger limits. API errors are:

```json
{"error":{"code":"validation_failed","message":"check the highlighted fields","details":{"name":"required"},"request_id":"..."}}
```

Switch behavior on `error.code`, map `details` to fields, and show the request
ID in unexpected-error support UI. A 401 clears cached account state. A 404
may intentionally hide another tenant's resource. A 409 is a domain state to
handle explicitly (for example `stale_revision`, `quote_expired` or
`items_outside_room`), not a generic network failure.

List endpoints use opaque keyset cursors. Send `next_cursor` back byte-for-byte
as `cursor`, together with the same filters and sort; `null` means the end.
Catalog and product pages fetch in Server Components. TanStack Query is for
interactive account/design/editor/quote state and mutations, not public-page
data fetching.

### Design autosave revisions

Design detail, creation and every successful design/item mutation return an
aggregate `ETag` such as `"design-4"`. Send that value unchanged in `If-Match`
for PATCH/DELETE design requests and POST/PATCH/DELETE item requests. Mutations
are serialized per design and each success returns the next ETag, except
deleting the design itself: that answers `204` with no ETag, because no next
revision exists.

Missing `If-Match` is `428 precondition_required`; malformed values are
`400 validation_failed`. A competing tab/device produces `409 stale_revision`
and the response carries the current ETag. Refetch the design before rebasing
local edits. Ownership is resolved first, so another customer's design remains
a 404 regardless of the supplied revision.

## Money, dimensions and coordinates

- Money is an integer `amount_minor`/`price_minor` plus an ISO currency. Never
  calculate with display decimals; format at the UI boundary.
- Catalog dimensions remain integer millimetres in forms, API data and editor
  state. Convert only for rendering: `threeValue = millimetres / 1000`.
- The room origin is the north-west floor corner. +X points east, +Z south and
  +Y up. A placed item's position is the centre of its floor footprint.
- `rotation_deg` is counter-clockwise when seen from above (+Y up). Map it
  directly with `object.rotation.y = THREE.MathUtils.degToRad(rotation_deg)`.
  Keep an editor unit test for this sign convention.
- The backend validates only that an item's centre is inside the room. The
  editor additionally clamps its rotated rectangular footprint to the walls:
  `halfX = |cos θ|w/2 + |sin θ|d/2` and
  `halfZ = |sin θ|w/2 + |cos θ|d/2`. There is no collision detection.

## GLB and configurable materials

Every product model is a self-contained glTF 2.0 GLB, authored in metres,
Y-up and real size. Its local X/Z footprint is centred on the origin and its
lowest point is at Y=0. The frontend may normalize a bad pivot for display but
must never resize geometry to catalog dimensions.

Configurable models use `KHR_materials_variants`:

1. Every sellable backend SKU has one KHR variant with exactly the same
   case-sensitive name.
2. Selecting a backend variant applies the KHR mapping named by its SKU.
3. Each option value's `material_key` names an embedded GLB material.
4. Every material used by every variant is embedded and referenced by a KHR
   mapping; exporters must not discard unused alternatives.

The development seed GLB follows this contract. In development, compare the
loaded bounding box (X width, Z depth, Y height) with `width_mm`, `depth_mm`
and `height_mm`. Log the product/model identity and per-axis values if any
relative difference exceeds 5%; do not auto-correct scale. A seller-side size
check beside `material_check` is deferred backend work.

Public models/images use the response URLs, normally `/v1/media/:id`. They are
immutable and support range requests. Show the primary image when a model is
missing, WebGL is unavailable or GLB loading fails.

## Main customer flows

1. **Browse:** categories → filtered `/v1/catalog/products` pages → public
   product detail by company/product slug.
2. **Configure:** derive valid option choices from listed variants; disabled or
   unavailable combinations cannot be placed. Apply the selected SKU's KHR
   variant in the preview.
3. **Design:** authenticate → create from a room template/custom dimensions →
   POST an item with `If-Match` → keep drag/rotation local → PATCH once an
   interaction commits → advance the returned ETag → show saving/saved/retry
   state → refetch estimates after structural changes.

`DesignItem.product.sku` is the exact case-sensitive variant SKU, or `null`
for a product without variants. Use it directly as the KHR variant name when
rendering placed furniture; do not infer a SKU from option display names.
4. **Quote:** submit a design → show requests created per seller and skipped
   unavailable items → display revisions/expiry → send the displayed revision
   when accepting and refetch on `stale_revision`.

## Versioning

`/v1` is additive-only after frontend development begins. Existing fields,
meanings and operations are not removed or incompatibly changed. Breaking
contracts use `/v2`.
