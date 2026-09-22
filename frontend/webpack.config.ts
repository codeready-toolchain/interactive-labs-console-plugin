import { ConsoleRemotePlugin } from "@openshift-console/dynamic-plugin-sdk-webpack";
import type { Configuration } from "webpack";

const config: Configuration = {
  mode: "development",
  entry: {},
  plugins: [new ConsoleRemotePlugin()],
  devtool: "source-map",
  resolve: {
    conditionNames: ["browser", "..."],
    extensions: [".ts", ".tsx", ".js"],
  },
  externals: ({ request }, callback) => callback(null, request?.startsWith("node:") ? `module ${request}` : undefined),
  output: { environment: { dynamicImport: true } },
  module: {
    parser: { javascript: { url: false } },
    rules: [
      { test: /\.tsx?$/, use: "ts-loader", exclude: /node_modules/ },
      // PatternFly 6 ESM imports React 17's extensionless JSX runtime path.
      { test: /\.m?js$/, include: /node_modules[\\/]@patternfly[\\/]/, resolve: { fullySpecified: false } },
    ],
  },
};

export default config;
