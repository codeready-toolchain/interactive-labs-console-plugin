// Minimal stub of @openshift-console/dynamic-plugin-sdk for unit tests.
// Replace with jest.fn() override per test file as needed.

export const consoleFetch = jest.fn();
export const useActiveNamespace = jest.fn((): [string, (namespace: string) => void] => ['#ALL_NS#', jest.fn()]);
export const isAllNamespacesKey = (namespace: string): boolean => namespace === '#ALL_NS#';
