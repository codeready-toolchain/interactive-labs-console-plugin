import { ConsoleRemotePlugin } from "@openshift-console/dynamic-plugin-sdk-webpack";
import type { Configuration } from "webpack";

const config: Configuration = {
  mode: "development",
  entry: {},
  plugins: [new ConsoleRemotePlugin()],
  devtool: "source-map",
  resolve: { extensions: [".ts", ".tsx", ".js"] },
  module: {
    rules: [{ test: /\.tsx?$/, use: "ts-loader", exclude: /node_modules/ }],
  },
};

export default config;
