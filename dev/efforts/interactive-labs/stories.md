# Interactive Labs — Stories

Stories for [proposal.md](proposal.md). These are local planning records only; no Jira issues have been created or updated.

## ☑ STORY-1: Establish secure plugin, backend, and lab-bundle contract

Interactive Labs needs a safe Console-to-backend boundary and a stable content contract before UI work can load a lab.

**Goal:** Deploy plugin and backend boundary that lists an admin-approved catalog and returns validated, versioned lab bundles.

**Acceptance criteria:**
- Dynamic plugin uses declared HTTPS `UserToken` Console service proxy.
- Backend lists only admin-configured approved catalog entries.
- Backend validates manifest `v1alpha1`, lab-tree paths, bundle contents, and typed errors.
- Backend derives repository, branch, lab path, and authorization identity; it never logs or persists forwarded tokens.

**Dependencies:** None.

## ☐ STORY-2: Render lab content in persistent accessible overlay

Users need readable instructions beside Console work, with predictable controls and safe navigation inside a loaded lab.

**Goal:** Let a user select and read a supported lab bundle in an accessible overlay that preserves browser-local panel state.

**Acceptance criteria:**
- User can select a catalog lab and launch its first page when no prerequisite chart is configured.
- Overlay supports keyboard-accessible open, minimize, restore, close, and reset actions with focus management.
- Close/reopen restores valid selected-lab, page, and panel state from browser local storage.
- Reset clears only browser-local Interactive Labs state and never alters cluster resources or setup activity.

**Dependencies:** STORY-1.

## ☐ STORY-3: Render restricted AsciiDoc and navigate lab-local pages

Lab authors need inter-linked instructional pages without allowing content to execute code, escape its bundle, or inject unsafe DOM into Console.

**Goal:** Render documented supported AsciiDoc subset and resolve only allowed lab-local content.

**Planning note:** Catalog `description` is AsciiDoc source. Client renders it through this story's
restricted, sanitised rendering path; STORY-1 only transports raw text.

**Acceptance criteria:**
- Asciidoctor.js renders supported fixture-lab content after DOM sanitization.
- Backend runtime attributes override author-defined attributes.
- Allowed relative pages and lab-local includes resolve within validated bundle paths.
- Remote includes, executable extensions, arbitrary filesystem access, unsupported raw HTML, malformed content, and missing pages fail safely with actionable UI states.

**Dependencies:** STORY-1, STORY-2.

## ☐ STORY-4: Select namespaces and deploy prerequisite charts

Labs with prerequisites must target namespaces only with user intent and use current-user RBAC rather than browser-side or privileged Helm execution.

**Goal:** Bind required existing namespaces explicitly, then run bounded synchronous Helm setup before opening chart-backed labs.

**Acceptance criteria:**
- Release namespace and annotated `values.schema.json` namespace inputs each require explicit confirmation.
- Console current namespace is preselected only when accessible and permitted; plugin never creates namespaces or chooses silently.
- Backend revalidates bindings, ownership, catalog, and chart inputs; it derives Helm release identity server-side.
- Helm setup runs with forwarded user's RBAC, waits within bounded timeout, and returns typed success, failure, or timeout results without automatic retry or cleanup.

**Dependencies:** STORY-1, STORY-2.

## ☐ STORY-5: Guide users to supported Console targets

Instructions must direct users to relevant Console UI without binding content to private selectors or unsafe arbitrary routes.

**Goal:** Support typed Quick Start-compatible guidance links with safe route navigation and optional target spotlighting.

**Acceptance criteria:**
- Guidance links accept only validated same-Console routes and supported symbolic target IDs.
- Plugin navigates before waiting for delayed `data-quickstart-id` target rendering.
- Found targets scroll into view and show accessible spotlight that dismisses on interaction, Escape, route change, or timeout.
- Missing or unsupported targets fall back to route-only navigation; arbitrary selectors and external navigation are rejected.

**Dependencies:** STORY-2, STORY-3.

## ☐ STORY-6: Record explicit local checkpoints

Users need visible progress without claiming that page visits or cached state prove cluster work completed.

**Goal:** Persist explicit local section checkpoints, initially `type: manual`, as browser-local user attestation.

**Acceptance criteria:**
- MVP accepts only `type: manual` checkpoints; sections expose their explicit user-confirmation controls.
- Manual checkpoint confirmation produces local `Confirmed` state only; navigation and page rendering never confirm a section.
- State is scoped by lab identity and configured branch, persists across panel close/reopen, and is discarded on reset or incompatible manifest.
- UI clearly distinguishes manual confirmation from automated verification, which is out of scope.

**Dependencies:** STORY-2, STORY-3.

## ☐ STORY-7: Publish and harden representative end-to-end lab

MVP must prove complete user flow and give operators and authors enough information to deploy, support, and recover from predictable failures.

**Goal:** Deliver one supported lab plus automated coverage and operational documentation for complete MVP path.

**Acceptance criteria:**
- Fixture lab works end to end: catalog selection, content, namespace confirmation, optional setup, navigation, guidance, and `type: manual` checkpoints.
- Unit, component, accessibility, proxy/backend integration, and OpenShift smoke tests cover expected success and failure states.
- Documentation defines supported content subset, catalog/manifest/chart conventions, proxy configuration, RBAC, Console compatibility range, and recovery steps.
- Security checks cover bundle paths, rendered content, routes, namespace boundaries, and token handling.

**Dependencies:** STORY-3, STORY-4, STORY-5, STORY-6.
