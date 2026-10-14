# Design decisions

Status: decisions below describe target MVP unless explicitly marked **implemented and verified**.

## Q1 — Current-user proxy authorization

**Decision:** Use Console `UserToken` proxy and one supported Console/Dynamic Plugin SDK compatibility range.

**Why:** preserves current-user RBAC and avoids privileged identity/impersonation. Forwarded tokens are never logged, persisted, audited, echoed, or returned in errors; current asynchronous setup worker holds token in memory beyond POST response for current-user cluster operations.

**Implemented and verified:** proxy alias `backend` uses `UserToken`; backend authenticates token against OpenShift user API.

## Q2 — Failed setup handling

**Decision:** recover abandoned operations with best-effort renewable Lease exclusion; never blindly reinstall over an existing Helm release. Setup resumes from verified SRs on a fresh authenticated POST when no release exists; verified safe interrupted `pending-install` releases can be explicitly torn down before relaunch.

**Why:** waiting forever after backend restart is unacceptable, but an expired Lease does not fence an in-flight Helm call. A 20-second renewal and 200-second takeover window bound the common crash while acknowledging rare overlap risk; resource identity checks and current-user RBAC still apply. Unsafe hooks, retained resources, or ambiguous Helm state remain blocked rather than silently deleted.

**Implemented:** async SpaceRequests/Helm setup and explicitly confirmed asynchronous teardown of dedicated identity-marked setups, including removed-catalog labs. Both use in-memory current-user tokens and renewable per-user/lab Leases; browser polling retries setup on next authenticated request when safe. Teardown stores a non-secret operation receipt on the Lease and reports expired running work as unknown. **Deferred:** unattended resume without current-user token, guaranteed fencing, durable execution journals, automated cleanup of unsafe or partial Helm states.

## Q3 — Rendered content safety

**Decision:** restricted AsciiDoc authoring subset plus DOM sanitization.

**Why:** trusted repository delivery does not make generated HTML safe for Console. Remote includes, executable extensions, arbitrary filesystem access, unsupported raw HTML, plugin backend-resource navigation, and arbitrary selectors are prohibited. Ordinary external HTTP(S) links open a safe new tab; same-origin Console links use same-tab Console navigation while drawer remains open.

**Implemented and verified:** renderer and sanitizer implementation. Author capability expands only after security review.

## Q4 — Console guidance links

**Decision:** `console:<quickstart-data-id>[label]` accepts only a fixed 25-ID Console registry; authors supply neither routes nor selectors. `resource:<apiVersion>/<Kind>[label,scope=namespaced|cluster,name=...,namespace=...]` requires two or three GVK components (`version/kind` or `group/version/kind`) and uses one ordinary attribute list with the label first and optional named fields after it. It accepts literals, full final-attribute references, or safe mixed `{attribute}` interpolation in `name` and `namespace`; scope and attribute schema remain enforced, cluster scope rejects `namespace`, and every GVK component and nonempty supplied selector is URI-encoded into fixed derived Console list or details paths. Console resolves resource existence; authors control neither routes nor filter syntax.

**Why:** avoids author-controlled routes/selectors while preserving ordinary same-origin Console links. Guidance first spotlights visible targets or reveals targets through validated collapsed nav ancestors. Route-backed IDs navigate once only when neither is available; visible-only IDs never infer or navigate a route. Missing targets announce unavailability, with route-backed targets left at their fixed route if fallback navigation occurred.

**Implemented and verified:** renderer emits registry-derived Console anchors with per-render provenance metadata and validated resource URLs. Guidance revalidates that metadata, ID, and href before querying exact `data-quickstart-id` nodes (with the two Topology markers as aliases); it uses a non-modal spotlight, dismissal/cancellation, and conditional persistent drawer minimization. It may click only a hidden target's structurally validated PatternFly nav ancestor disclosure buttons, outermost-first, waiting for React expansion commits and leaving sections open. Destination targets are never activated; malformed relationships and hidden sidebars are not opened. No private Console imports, destination target activation, route discovery, selector input, backend API, or client-side resource/RBAC query was added.

## Q5 — Completion evidence, checkpoints, and section solving

**Status: open.** Evidence rules, checkpoint semantics, automated verification, and section-solver behavior need a shared model before they become MVP contract.

**Provisional direction:** browser-local `type: manual` checkpoint as user attestation. It avoids premature cluster-query/check DSL, but does not prove instruction completion. Page navigation must not claim completion.

## Q6 — Progress durability

**Decision:** browser-local state only.

**Why:** avoids new storage, identity, privacy, retention, audit, and reset systems. State is scoped by lab identity and configured branch; reset clears browser data only.

**Tradeoff:** no cross-device resume or audit history.

## Q7 — Namespace selection and lifecycle

**Status: open.** Product needs policy for one and multiple namespaces.

**Provisional direction:** explicit existing release-namespace selection plus `values.schema.json` fields annotated `x-interactive-labs: { kind: namespace-name }` for additional inputs. This is not current setup behavior: current chart setup creates SpaceRequests from schema annotations, then installs Helm asynchronously. Selection/lifecycle policy remains open.

## Q8 — Manifest/API evolution

**Decision:** keep `apiVersion: v1alpha1` while deliberately breaking its old flat lab shape. This is an explicit incompatible cutover, not additive evolution: `lab.yaml` now requires `metadata.title`, rejects old name/displayName and root checkpoints, and pages live in `modules/<id>/content/`; bundle has modules, not root pages. Backend and plugin reject unsupported versions **and** old/unknown fields. No general migration framework.

**Implemented and verified:** backend and browser validate new manifest/bundle shapes. Authors must convert old flat labs before rollout. Invalid cached old snapshots are omitted safely until a later successful refresh replaces them; active sessions are not migrated. Change to a new `apiVersion` if future compatibility promises require coexistence rather than treating this breaking `v1alpha1` change as additive.

## Q9 — Lab-content delivery

**Decision:** catalog references approved Red Hat HTTPS repository branches; backend loads manifest and page descriptors at launch, then browser fetches approved resources on demand. Optional chart dependencies may resolve from sources declared by approved chart.

**Why:** centrally maintained branches avoid customer ConfigMap changes for each content update.

**Tradeoff:** Service-account refresh stages and replaces changed branch snapshots on validated cadence or after catalog ConfigMap changes, while cached-resource responses expose current generation Git `HEAD` as HTTP `ETag`. MVP has no revision identity in catalog, bundle, or session data; no update notification, pinning, migration, rollback, or running-session reconciliation.

## Cross-cutting decisions

- `catalogRef` is opaque SHA-256 over repository, branch, lab path, and catalog attribute overrides. Display fields and runtime attributes do not affect identity.
- Attribute precedence is manifest, then catalog overrides, then backend runtime attributes. Runtime attributes do not affect catalog identity or Helm release name.
- Helm release identity is backend-derived from authenticated user, unchanged `catalogRef`, normalized repository-relative effective chart path, and sorted namespace binding keys. Runtime attributes, absolute cache locations, revisions, chart contents, and raw override spelling do not contribute. Shared chart files retain separate installed user/lab releases; no pre-release migration/cleanup is added.
- Chart content mechanics and typed schema decoding belong to shared `chart`; `bundle` loads content and validates that contract early, never provisioning policy. `setup` owns preparation requirements, decoded binding interpretation, release identity, and `Plan`. Server composes these operations against one existing approved snapshot cache; no second cache or provisioning operation lives in `bundle`.
- Use one unified frontend/backend image and one HTTPS Service.
- Use typed Kubernetes and OpenShift clients for all cluster API access.

## Open or deferred

Namespace selection/lifecycle, completion evidence/checkpoint rules, and section-solving behavior are open. Current explicit existing-namespace selection and browser-local manual-checkpoint approaches are provisional, not current UI. No commitment exists for automated verification, author-validation tool, durable/audited progress, durable async recovery, upstream revision lifecycle, rollback, disconnected delivery, or broad chart topology support.

## Decision records

Detailed open-question records, provisional directions, and resolved decisions live in [architect notes](../efforts/interactive-labs/architect-notes.md).
