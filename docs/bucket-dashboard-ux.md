# Ember Bucket Dashboard — User Flow and UX Design Brief

Status: design gate before implementation
Route: tenant bucket resource page (`/tenants/{tenant}/resources/{bucket}?scope={parent}`)
Audience: an operator who already knows what a bucket and object are
Primary surface: Operate / Inspect

## Evidence boundary

- Source files: `internal/ember/web.go`, `internal/ember/web_assets.html`, `internal/ember/web_assets.css`, `internal/ember/web_test.go`.
- Existing live behavior: bucket resource page lists objects, accepts multipart upload, downloads by object key, and deletes with explicit confirmation.
- Existing rendered state inspected: desktop populated bucket dashboard with one object; desktop empty state was also inspected earlier. Browser console had no errors.
- Existing limitations observed: no post-action success notice, no object filtering or bulk-selection flow, and the generic resource creation form previously relied on select dropdowns.
- Public preview state is disposable and must remain separate from production state.

## User jobs

### Job 1: Locate and orient

“I opened this resource. Tell me immediately which bucket this is, which tenant/scope owns it, what state it is in, how much data it contains, and where the data actions are.”

Success condition: within five seconds the operator can identify resource context, current state, object count/size, and the primary data action without reading a paragraph.

### Job 2: Upload one object

“I have a local file. Put it in this bucket under a deliberate object key and tell me whether it succeeded.”

Success condition: one visible Upload action leads to a compact form with persistent labels for key and file; after submission the result is explicit and the new object is visible in the same table.

### Job 3: Inspect or retrieve an object

“I need to find an object, confirm its size/integrity, and download it.”

Success condition: the object table is the main content surface, names are recognizable as links, metadata is scannable, and Download is available without guessing where the row leads.

### Job 4: Remove an object safely

“I need to remove exactly this object, not the bucket.”

Success condition: object deletion is clearly distinct from bucket deletion, requires explicit confirmation, and returns to the same object list with visible result feedback.

### Job 5: Inspect control-plane details

“I need the resource ID, desired/observed state, tags, and recent activity without leaving the resource.”

Success condition: control-plane metadata is available as a compact secondary section and does not compete with the object data plane.

## User flow

```text
Tenant resources
  -> open bucket resource
  -> orient: name / scope / desired + observed state / object count + bytes
  -> choose one of the primary actions:
       Upload -> choose file + enter key -> submit -> success/error feedback -> object appears in table
       Objects -> scan table -> open/download object
       Properties -> inspect compact configuration and tags
  -> destructive branch:
       row Delete -> confirm exact object -> delete -> success feedback -> row removed
       danger zone Delete bucket -> separate confirmation -> resource deletion
```

## Cloud-console case study findings

### AWS S3 — object vocabulary and direct object actions

**Documented behavior:** S3 frames the model as buckets containing objects; users upload objects to a bucket and then open, download, copy, or move them.[1]

**Ember decision:** keep `bucket` and `object` as the visible nouns. Make the object list the primary work surface and keep object actions attached to the object row. Do not introduce a separate “blog” or generic “file manager” abstraction.

**Compatibility trap:** a bucket page that only exposes tags and resource deletion is control-plane metadata, not an object-store experience.

### Google Cloud Storage — table-first browsing

**Documented behavior:** the Cloud Storage console has bucket/object list surfaces and supports filtering/sorting lists of buckets and objects.[2]

**Ember decision:** use a dense table with stable columns, object count, bytes, and a clear empty state. Add filtering only when it has a real server/query contract; do not render a decorative search box that does nothing.

**Conscious simplification:** Ember’s first slice does not add drag-and-drop or bulk selection. The single-object flow must still be complete and clear.

### Azure Blob Storage — resource hierarchy plus upload as the next action

**Documented behavior:** Azure’s portal flow is storage account -> container -> blob list; the quickstart explicitly lists upload, download, and delete as the basic portal operations.[3]

**Ember decision:** preserve the resource hierarchy in the header/side navigation, but make Upload the strongest action on the bucket page. Keep bucket deletion visually and semantically separate from object deletion.

**Conscious simplification:** Ember’s `bucket` is the current storage resource boundary; it does not invent a second container screen until the backend supports multiple containers per bucket.

### NN/G usability heuristics — interaction rules

The flow applies visibility of system status, match between the system and real-world object vocabulary, user control/freedom, consistency, error prevention, recognition rather than recall, and aesthetic/minimalist design.[4]

**Ember decisions:** show desired and observed state as real values; use bucket/object language; preserve the current scope in every action URL; keep destructive confirmation explicit; use persistent field labels; do not hide the primary task behind dropdowns or unexplained navigation.

## Information hierarchy

1. **Resource context:** breadcrumb, bucket name, tenant/scope, desired/observed state.
2. **Primary command strip:** Upload, Objects, Properties.
3. **Glanceable real data:** object count, bytes used, per-object limit, integrity policy.
4. **Primary workbench:** object upload form and object table.
5. **Secondary evidence:** configuration/tags and activity.
6. **Recovery/destruction:** error feedback, empty state, object delete, bucket danger zone.

## Interaction and state contract

### Required states

- Empty bucket: explain that no objects exist and point to Upload.
- Populated bucket: table with key, size, checksum, and object actions.
- Upload success: redirect back to the bucket with a visible success notice naming the key, without exposing file contents.
- Upload validation error: retain the entered key where safe and identify the affected field; never claim success.
- Object delete success: redirect with a visible notice naming the deleted key.
- Bucket delete: remains separately confirmed and destructive.
- Narrow viewport: navigation becomes a compact horizontal rail; table may scroll horizontally, but the page must not require two-dimensional scrolling for the whole surface.
- Keyboard: native links, radios, inputs, buttons, visible focus, logical order.

### Explicitly not included in this slice

- fake charts or invented health metrics;
- a search box without a filtering implementation;
- bulk selection without bulk-operation semantics;
- drag-and-drop without a tested upload path;
- a separate container-management layer not supported by the current backend;
- secrets or credential values in the UI.

## Unslop UI audit gate

| Area | Status | Exact evidence | Checklist IDs | Smallest safe action |
|---|---|---|---|---|
| Surface and hierarchy | FLAGGED | Current source combines resource controls, object data, configuration, activity, and deletion in one generic branch; the bucket-specific route must keep the object data plane dominant. | 5, 6, 48, 50 | Preserve the resource rail but make the object table and its actions the primary workbench; keep control-plane sections compact. |
| Eyebrows, labels, badges, subtitles, supporting text | FLAGGED | Current markup contains `Storage resource`, `Bucket`, `Local blob storage`, `Data plane`, `Resource settings`, `Traceability`, and repeated explanatory paragraphs. Some are useful scope/status labels; others repeat the section heading. | 24, 34, 60 | Keep only category/status/scope text that adds information; remove repeated prose and use direct task labels. |
| Copy and content truth | FLAGGED | Current `Activity log` can show `0 operations` and an empty panel; upload has no success notice after redirect; `1 objects` appeared in the rendered snapshot before copy correction. | 62, 86, 89, 99 | Add explicit success/error feedback, correct plurality, and keep empty states actionable. |
| Decoration and visual convergence | NOT FLAGGED after prior pass | Current CSS has no gradients, glass, glow, fake charts, or decorative animation; the rail and command strip have structural meaning. | 1, 2, 5, 6, 18, 35 | Do not add visual novelty. Reduce any remaining nested framing only where it obscures the table. |
| Typography and design-system drift | NOT FLAGGED | Existing route uses a coherent system sans, mono for IDs/checksums, restrained navy/blue semantic palette, and consistent radii. | 7, 12, 13, 14, 15 | Preserve tokens and type roles; do not introduce a second UI kit or display font. |
| States, interaction, responsive behavior, accessibility | FLAGGED | Existing source has native controls and focus styles, but no success notice, no field-specific upload error rendering, and narrow viewport verification remains outstanding. | 82, 85, 86, 88, 89, 98, 100 | Implement feedback states, preserve labels, verify narrow layout and keyboard flow after the design changes. |

## Auxiliary-text inventory

| Selector/component | Exact text | Classification | Decision |
|---|---|---|---|
| `.sidebar-kicker` | `Storage resource` | MEANINGFUL | Keep: identifies the resource family in the rail. |
| `.sidebar-context span` | `Parent scope` | MEANINGFUL | Keep: scope is operational context. |
| `.resource-kicker` | `Bucket` | MEANINGFUL | Keep: resource type. |
| `.resource-subtitle` | `Desired … · Observed … · Local blob storage` | MEANINGFUL | Keep, but show only real state values and concise separators. |
| `.command-item small` | `Add a blob`, `View objects`, `Inspect settings` | MEANINGFUL | Keep: action outcome, not marketing copy. |
| `.section-label` | `Data plane` | UNCLEAR | Prefer direct `Objects` heading; do not require infrastructure jargon to find the work surface. |
| `.panel-description` | `Manage the blobs stored in {name}.` | REDUNDANT | Replace with the table context/count or omit when the heading is sufficient. |
| `.section-label` | `Resource settings` | MEANINGFUL | Keep: distinguishes control-plane metadata. |
| `.section-label` | `Traceability` | GENERIC | Replace with `Activity log`. |
| `.danger-panel .section-label` | `Danger zone` | MEANINGFUL | Keep: destructive context. |
| `.upload-copy p` | `Choose a file and assign the key Ember will use to store it.` | MEANINGFUL | Keep as a concise instruction beside the upload form. |
| `.empty-state p` | `This bucket has no stored blobs. Use the upload form above to add your first object.` | MEANINGFUL | Keep: explains state and next action. |

## Design acceptance criteria

- No `<select>` or disclosure control is required for the primary resource-creation path; resource type and desired state use visible, keyboard-operable choices.
- The bucket route has one obvious primary action: Upload object.
- The object table is the largest and most information-dense work surface.
- Every destructive operation remains explicit and separately scoped.
- Upload and delete outcomes are visible after redirect.
- No invented metrics, charts, or provider capabilities appear.
- The route renders correctly with `/ember` forwarded prefix and without it.
- Desktop and narrow viewport screenshots are inspected; browser console has no errors.

## Sources

[1] AWS S3 object workflow: https://docs.aws.amazon.com/AmazonS3/latest/userguide/uploading-downloading-objects.html
[2] Google Cloud Storage console list/filter/object workflow: https://docs.cloud.google.com/storage/docs/cloud-console
[3] Azure portal blob upload/download/list/delete workflow: https://learn.microsoft.com/en-us/azure/storage/blobs/storage-quickstart-blobs-portal
[4] Nielsen Norman Group usability heuristics: https://www.nngroup.com/articles/ten-usability-heuristics/

## Implementation closeout

- Replaced resource-type and desired-state dropdowns with visible native radio choices; provider metadata is inline and labeled.
- Kept the bucket dashboard focused on the object data plane, with cloud-console-inspired resource context, command actions, dense metrics, object table, compact configuration, activity, and separate destructive zone.
- Added redirect-backed success notices for upload and object deletion without exposing object contents.
- Removed redundant `Data plane`/`Traceability` auxiliary labels in favor of direct `Bucket contents`/`Activity` language.
- Preserved the existing `/ember` prefix behavior, object upload/download/delete contracts, explicit destructive confirmation, and local-only boundary.
- Verified `go test ./internal/ember/...`, `make build`, the live upload/list/download/delete flow, the clean post-delete row state, no `<select>` markup, browser console with zero errors, and the rendered desktop bucket and tenant creation surfaces.
- Narrow viewport and keyboard traversal remain part of the acceptance boundary; native semantic controls and responsive CSS are present, but a dedicated narrow screenshot/keyboard session was not available in this browser runner.
