# Interactive Labs

**Effort:** `interactive-labs`  
**Jira:** [SANDBOX-1998](https://redhat.atlassian.net/browse/SANDBOX-1998)

## Problem

OpenShift Console users need in-context, guided instructions for accomplishing cluster tasks. Existing documentation cannot prepare required cluster resources or direct users to exact Console areas while they work.

## Goal

Deliver an OpenShift Dynamic Plugin that presents an interactive lab inside Console. A user can open a lab, follow linked Asciidoctor instruction pages in a minimizable and closeable overlay, use links that highlight relevant Console UI, and work against lab resources deployed before starting.

## Scope

- Render multi-page, inter-linked Asciidoctor lab content in Console.
- Provide step navigation and persistent open/minimized/closed panel controls.
- Support lab links that highlight target Console sections, matching OpenShift Quick Starts behavior.
- Deploy Helm-chart-defined prerequisite resources before lab begins.
- Let a user complete one end-to-end lab using supported content, navigation, Console guidance, and prepared resources.

## Open design decisions

- Namespace selection and lifecycle for charts requiring one or multiple namespaces. Current explicit selection of existing namespaces is a provisional direction, not a settled policy.
- Evidence and rules for determining whether a user completed instructions. Current local `type: manual` checkpoint is a provisional direction, not a completion model.
- What it means to solve a lab section for a user, including whether actions are automated.

## Non-goals

- Define automated lab-completion or section-solving behavior before its model is agreed.
- Solve every possible Helm or namespace topology in initial delivery.
- Replace Console documentation or OpenShift Quick Starts outside lab integration.

## Acceptance criteria

1. User can launch a lab from OpenShift Console and read its multi-page Asciidoctor content in an overlay.
2. User can navigate linked pages and minimize, restore, or close overlay without losing expected state.
3. A lab link can highlight intended Console location.
4. Lab prerequisite Helm resources are deployed before user follows instructions.
5. One representative lab is usable end to end.
