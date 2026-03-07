import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "path";

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  build: {
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes("node_modules")) {
            if (id.includes("react-dom") || id.includes("/react/") || id.includes("react-router")) {
              return "vendor-react";
            }
            if (id.includes("@xterm")) {
              return "vendor-xterm";
            }
            if (id.includes("@radix-ui")) {
              return "vendor-ui";
            }
            if (id.includes("react-markdown") || id.includes("rehype") || id.includes("remark") || id.includes("unified") || id.includes("mdast") || id.includes("hast") || id.includes("micromark")) {
              return "vendor-markdown";
            }
            if (id.includes("lucide-react")) {
              return "vendor-icons";
            }
            if (id.includes("@tanstack")) {
              return "vendor-query";
            }
            if (id.includes("highlight.js") || id.includes("lowlight")) {
              return "vendor-highlight";
            }
            if (id.includes("reactflow") || id.includes("@reactflow") || id.includes("dagre")) {
              return "vendor-flow";
            }
          }
        },
      },
    },
  },
  server: {
    proxy: {
      "/api": {
        target: `http://localhost:${process.env.PORT || 9040}`,
        changeOrigin: true,
        ws: true,
      },
      "/ws": {
        target: `ws://localhost:${process.env.PORT || 9040}`,
        ws: true,
      },
    },
  },
});
