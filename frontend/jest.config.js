/** @type {import('jest').Config} */
module.exports = {
  preset: 'ts-jest',
  testEnvironment: 'jsdom',
  roots: ['<rootDir>/src'],
  moduleFileExtensions: ['ts', 'tsx', 'js'],
  moduleNameMapper: {
    // redirect consoleFetch to a lightweight stub so SDK need not be built
    '@openshift-console/dynamic-plugin-sdk': '<rootDir>/src/__mocks__/dynamic-plugin-sdk.ts',
    '^@asciidoctor/core$': '<rootDir>/src/__mocks__/asciidoctor.ts',
    '^mermaid$': '<rootDir>/src/__mocks__/mermaid.ts',
    // stub CSS imports from @patternfly/react-styles (Console supplies them at runtime)
    '\\.(css|scss|sass|less)$': '<rootDir>/src/__mocks__/fileMock.js',
  },
  transform: {
    '^.+\\.tsx?$': ['ts-jest', {
      tsconfig: {
        module: 'CommonJS',
        moduleResolution: 'node',
      },
    }],
  },
};
