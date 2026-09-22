const mermaid = {
  initialize: jest.fn(),
  render: jest.fn(async (_id: string, source: string) => {
    if (source.includes("not a diagram")) throw new Error("invalid diagram");
    return { svg: '<svg onload="alert(1)"><style>.node{fill:#fff}</style><script>alert(1)</script><g class="node" style="stroke:#000"><text>Browser</text><text>diagram</text></g></svg>' };
  }),
};

export default mermaid;
