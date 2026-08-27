// Phase B — Error envelope parsing.
// These tests fail until api/client.ts (or api/errors.ts) exports parseLabError.

import { parseLabError } from "../client";

describe("parseLabError", () => {
  it("returns body unchanged when kind is Error and type is known", () => {
    const body = { apiVersion: "v1alpha1", kind: "Error", type: "lab_not_found", message: "missing" };
    const result = parseLabError(body);
    expect(result.kind).toBe("Error");
    expect(result.type).toBe("lab_not_found");
  });

  it("normalises unknown type to unknown_error", () => {
    const body = { apiVersion: "v1alpha1", kind: "Error", type: "some_future_type", message: "x" };
    const result = parseLabError(body);
    expect(result.type).toBe("unknown_error");
  });

  it("normalises missing kind to unknown_error", () => {
    const body = { apiVersion: "v1alpha1", kind: "SomethingElse", message: "x" };
    const result = parseLabError(body);
    expect(result.type).toBe("unknown_error");
  });

  it("handles null / non-object body", () => {
    expect(parseLabError(null).type).toBe("unknown_error");
    expect(parseLabError("plain string").type).toBe("unknown_error");
    expect(parseLabError(42).type).toBe("unknown_error");
  });

  it("preserves optional detail field", () => {
    const body = { apiVersion: "v1alpha1", kind: "Error", type: "unauthorized", message: "no", detail: { code: 401 } };
    const result = parseLabError(body);
    expect(result.detail).toEqual({ code: 401 });
  });
});
