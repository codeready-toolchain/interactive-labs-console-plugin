# Interactive Labs — Stories

Stories for [proposal.md](proposal.md). These are local planning records; Jira issues are tracked in `.jira.properties`.

## ☑ STORY-1: Establish secure plugin, backend, and lab-bundle contract

Interactive Labs needs a safe Console-to-backend boundary and a stable content contract before UI work can load a lab.

**Goal:** Deploy plugin and backend boundary that lists an admin-approved catalog and returns validated, versioned lab bundles.

**Acceptance criteria:**
- Dynamic plugin uses declared HTTPS `UserToken` Console service proxy.
- Backend lists only admin-configured approved catalog entries.
- Backend validates manifest `v1alpha1`, lab-tree paths, bundle contents, and typed errors.
- Backend derives repository, branch, lab path, and authorization identity; it never logs or persists forwarded tokens.

**Dependencies:** None.

## ☑ STORY-2: List available labs

Users need a clear catalog of administrator-approved labs before any lab can be launched.

**Goal:** Render catalog entries on `InteractiveLabsPage` with a deliberately disabled Launch control.

**Acceptance criteria:**
- Page loads and displays catalog lab names and optional descriptions through the existing proxy client.
- Catalog uses direct, Console-compatible PatternFly dependencies and components; it never relies on transitive PatternFly installation through Dynamic Plugin SDK.
- Loading, empty catalog, typed API error, and retry states are accessible and actionable.
- Each catalog entry exposes an inert disabled PatternFly `Launch` button with adjacent availability explanation.
- Story does not load a bundle, persist session state, deploy setup, or render a drawer or content.

**Dependencies:** STORY-1.

## ☑ STORY-3: Launch lab in persistent accessible sliding drawer

Users need to launch a selected supported lab beside Console work with predictable controls and local panel state.

**Goal:** Enable Launch and show a validated no-chart lab's entry page in a persistent accessible sliding drawer.

**Acceptance criteria:**
- Enabled Launch loads and validates selected lab bundle; a lab without a prerequisite chart opens its first navigable page.
- Drawer supports keyboard-accessible open, minimize, restore, close, and reset actions with focus management.
- Close/reopen restores valid selected-lab, page, and panel state from browser local storage. Reopen requires explicit user activation; route remount or browser refresh never auto-opens the drawer.
- Reset clears only browser-local Interactive Labs state and never alters cluster resources or setup activity.

**Dependencies:** STORY-1, STORY-2.

## ☑ STORY-4: Render restricted AsciiDoc and navigate lab pages

Lab authors need inter-linked instructions without content executing code, escaping its bundle, or injecting unsafe Console DOM.

**Goal:** Render supported AsciiDoc, navigate approved lab pages, and resolve same-repository resources.

**Planning note:** Catalog response exposes normalized server-derived `labPath`; client uses it only to derive the `lab.yaml` base for raw `metadata.description` AsciiDoc.

**Acceptance criteria:**
- Use exact Asciidoctor, DOMPurify, highlight.js, and Mermaid dependencies to render supported fixture content after sanitization; final backend attributes are hard-set in Asciidoctor.
- Bundle page descriptors carry backend-relative approved resource paths; client resolves them through Console proxy. Resource reads use a quoted cached Git-HEAD SHA as shared ETag with `private, no-cache`; resource-size caps are not yet enforced.
- Includes/images stay in approved repository. Relative links open only known descriptor pages in drawer; external HTTP(S) opens safe new tab; Console URLs navigate same tab without closing drawer; plugin resource URLs warn.
- Description page link validates bundle then explicitly launches requested page. Recoverable rendering/resource faults warn; fetch, conversion, and sanitization faults show safe actionable page error.

**Dependencies:** STORY-1, STORY-3, STORY-9.

## 🔧 STORY-5: Provision namespaces via SpaceRequests

Labs with prerequisites need namespace provisioning via SpaceRequests before the lab can be opened.

**Goal:** Automatically provision namespaces via CodeReady Toolchain SpaceRequests based on the lab's `values.schema.json` namespace declarations.

**Planning status:** Design agreed and documented in [lab-setup-design.md](../docs/lab-setup-design.md). Namespace provisioning via SpaceRequests and async setup flow (kick-and-poll) are implemented.

**Implemented:**
- Backend scans `setup/helm/values.schema.json` for `x-interactive-labs.kind: namespace-name` properties.
- Creates SpaceRequests (one per namespace property) in the user's home namespace (`<username>-dev`) using the user's token.
- `POST /setup` initiates provisioning and returns 202; `GET /setup/status` returns per-namespace and overall status.
- Frontend calls `setupLab`, polls `getSetupStatus` with a spinner, and opens the lab once all namespaces are provisioned.

**Acceptance criteria:**
- `values.schema.json` namespace annotations drive SpaceRequest creation (idempotent).
- Backend provisions namespaces using forwarded user's RBAC via SpaceRequests in the user's home namespace.
- Async setup flow: POST initiates, GET polls status, frontend shows spinner until ready.

**Dependencies:** STORY-1, STORY-3.

## ☑ STORY-6: Guide users to supported Console targets

Instructions must direct users to relevant Console UI without binding content to private selectors or unsafe arbitrary routes.

**Goal:** Support typed Quick Start-compatible guidance links with safe route navigation and optional target spotlighting.

**Acceptance criteria:**
- Guidance links accept only validated same-Console routes and supported symbolic target IDs.
- Plugin navigates before waiting for delayed `data-quickstart-id` target rendering.
- Found targets scroll into view and show accessible spotlight that dismisses on interaction, Escape, route change, or timeout.
- Missing or unsupported targets fall back to route-only navigation; arbitrary selectors and external navigation are rejected.

**Dependencies:** STORY-3, STORY-4.

## ☐ STORY-7: Record progress after checkpoint model decision

Users need visible progress without claiming that page visits or cached state prove cluster work completed.

**Goal:** Implement section-progress behavior after evidence and checkpoint rules are decided.

**Planning status:** Browser-local `type: manual` checkpoints are a provisional direction, not a settled completion model. Evidence rules, automated verification, and solver behavior remain open.

**Acceptance criteria:**
- Checkpoint types and confirmation semantics follow resolved, documented evidence rules.
- Navigation and page rendering never claim a section is complete without satisfying those rules.
- Any persisted state is scoped by lab identity and configured branch and is discarded on reset or incompatible manifest.
- UI distinguishes user attestation from automated verification when both concepts are supported.

**Dependencies:** STORY-3, STORY-4.

## ☐ STORY-8: Publish and harden representative end-to-end lab

MVP must prove complete user flow and give operators and authors enough information to deploy, support, and recover from predictable failures.

**Goal:** Deliver one supported lab plus automated coverage and operational documentation for complete MVP path.

**Acceptance criteria:**
- Fixture lab works end to end: catalog selection, content, resolved namespace-selection flow, optional setup, navigation, guidance, and resolved checkpoint behavior.
- Unit, component, accessibility, proxy/backend integration, and OpenShift smoke tests cover expected success and failure states.
- Documentation defines supported content subset, catalog/manifest/chart conventions, proxy configuration, RBAC, Console compatibility range, and recovery steps.
- Security checks cover bundle paths, rendered content, routes, namespace boundaries, and token handling.

**Dependencies:** STORY-4, STORY-5, STORY-6, STORY-7.

## ☑ STORY-9: Resolve catalog metadata from cached approved lab manifests

Catalog labels and descriptions must not drift from lab-authored metadata while
administrators retain control of approved source locations and static overrides.

**Goal:** Build catalog items and bundles from cached, validated repository snapshots, with reusable snapshot support for future resource reads.

**Acceptance criteria:**
- Catalog entries require only `repository`, `branch`, and `labPath`; optional
  `attributeOverrides` remains administrator-owned deployment policy.
- Backend derives unchanged `catalogRef` before reading remote metadata, then returns
  manifest `displayName` and `description`.
- Backend caches one immutable shallow-cloned snapshot per configured repository/branch;
  cache hits do not clone again, and catalog, bundle, and setup loading use same snapshot. STORY-4 owns the browser resource route.
- A cache miss validates configured manifests and returns valid items in configured order.
- An unreachable repository, missing manifest, or invalid lab omits only that lab;
  browser receives no unavailable-entry state or raw upstream failure detail.
- Backend logs safe structured failure context and emits bounded namespaced Warning Events
  against `interactive-labs-catalog`; invalid configuration remains `configuration_error`.
- Cached snapshots do not refresh remotely in this story; source changes appear only after
  cache loss until STORY-10 adds refresh.

**Dependencies:** STORY-1, STORY-2.

## ☐ STORY-10: Refresh cached approved lab sources

Cached approved sources must receive branch updates without repeated user-request clones.

**Goal:** Periodically replace cached repository snapshots through bounded backend refresh.

**Acceptance criteria:**
- Backend refreshes configured repository/branch snapshots on defined cadence without browser requests.
- It validates configured lab paths before atomically replacing a snapshot; readers never see partial updates.
- Failed refresh retains last known-good snapshot and emits safe structured logs and bounded Warning Events.
- Initial misses and sources with no valid snapshot remain unavailable only to their affected labs.
- Refresh introduces no user-facing update notification, revision pinning, migration, or session reconciliation.

**Dependencies:** STORY-9.
