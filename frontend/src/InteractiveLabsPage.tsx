import * as React from "react";
import {
  Alert,
  Button,
  DataList,
  DataListAction,
  DataListCell,
  DataListContent,
  DataListItem,
  DataListItemCells,
  DataListItemRow,
  DataListToggle,
  EmptyState,
  EmptyStateBody,
  PageSection,
  Spinner,
  TextInput,
  Title,
} from "@patternfly/react-core";
import { listCatalog } from "./api/client";
import type { CatalogEntry } from "./types/catalog";
import type { LabError } from "./types/errors";

type PageState =
  | { status: "loading" }
  | { status: "success"; items: CatalogEntry[] }
  | { status: "error"; message: string };

const InteractiveLabsPage: React.FC = () => {
  const [state, setState] = React.useState<PageState>({ status: "loading" });
  const [retry, setRetry] = React.useState(0);
  const [filter, setFilter] = React.useState("");
  const [expandedItems, setExpandedItems] = React.useState<Set<string>>(() => new Set());
  const filteredItems =
    state.status === "success"
      ? state.items.filter((item) =>
          `${item.displayName} ${item.description ?? ""}`
            .toLowerCase()
            .includes(filter.toLowerCase()),
        )
      : [];

  React.useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });
    void listCatalog().then(
      (result) => {
        if (!cancelled) setState({ status: "success", items: result.items });
      },
      (err: unknown) => {
        if (!cancelled)
          setState({
            status: "error",
            message: (err as LabError | undefined)?.message ?? "Unknown error",
          });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [retry]);

  return (
    <PageSection>
      <Title headingLevel="h1">Interactive Labs</Title>

      {state.status === "loading" && (
        <Spinner aria-label="Loading labs" />
      )}

      {state.status === "error" && (
        <>
          <Alert variant="danger" title={state.message} isInline isLiveRegion />
          <Button variant="link" onClick={() => setRetry((c) => c + 1)}>
            Retry
          </Button>
        </>
      )}

      {state.status === "success" && state.items.length === 0 && (
        <EmptyState titleText="No labs available">
          <EmptyStateBody>No labs are currently configured.</EmptyStateBody>
        </EmptyState>
      )}

      {state.status === "success" && state.items.length > 0 && (
        <>
          <div style={{ marginBottom: "var(--pf-t--global--spacer--md)" }}>
            <TextInput
              aria-label="Filter labs"
              type="search"
              value={filter}
              onChange={(_event, value) => setFilter(value)}
              placeholder="Filter by name or description"
            />
          </div>
          {filteredItems.length === 0 ? (
            <EmptyState titleText="No labs match your filter.">
              <EmptyStateBody>Try a different name or description.</EmptyStateBody>
            </EmptyState>
          ) : (
            <DataList aria-label="Available labs" isCompact isPlain>
              {filteredItems.map((item) => {
                const itemId = `lab-${item.catalogRef}`;
                const descriptionId = `${itemId}-description`;
                const isExpanded = expandedItems.has(item.catalogRef);
                return (
                  <DataListItem
                    key={item.catalogRef}
                    aria-labelledby={itemId}
                    isExpanded={isExpanded}
                  >
                    <DataListItemRow>
                      {item.description && (
                        <DataListToggle
                          id={`${itemId}-toggle`}
                          aria-controls={descriptionId}
                          aria-label={`Toggle description for ${item.displayName}`}
                          isExpanded={isExpanded}
                          buttonProps={{
                            onClick: () =>
                              setExpandedItems((current) => {
                                const next = new Set(current);
                                if (next.has(item.catalogRef)) next.delete(item.catalogRef);
                                else next.add(item.catalogRef);
                                return next;
                              }),
                          }}
                        />
                      )}
                      <DataListItemCells
                        dataListCells={[
                          <DataListCell key="name" width={5}>
                            <strong id={itemId}>{item.displayName}</strong>
                          </DataListCell>,
                        ]}
                      />
                      <DataListAction
                        id={`${itemId}-actions`}
                        aria-labelledby={itemId}
                        aria-label={`Actions for ${item.displayName}`}
                      >
                        <Button isDisabled>Launch</Button>
                        <span> Launching will be available in a future update (STORY-3).</span>
                      </DataListAction>
                    </DataListItemRow>
                    {item.description && (
                      <DataListContent
                        id={descriptionId}
                        aria-label={`Description for ${item.displayName}`}
                        isHidden={!isExpanded}
                      >
                        {item.description}
                      </DataListContent>
                    )}
                  </DataListItem>
                );
              })}
            </DataList>
          )}
        </>
      )}
    </PageSection>
  );
};

export { InteractiveLabsPage };
