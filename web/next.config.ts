import path from "node:path";

import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  outputFileTracingRoot: path.resolve("."),
  poweredByHeader: false,
  reactStrictMode: true,
};

export default nextConfig;
