import { defineConfig } from "vite-plus";
import formatOptions from "./scripts/format-options.json" with { type: "json" };

export default defineConfig({ fmt: formatOptions });
