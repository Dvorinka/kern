import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
	plugins: [react(), tailwindcss()],
	server: {
		port: 5173,
		proxy: {
			"/api": {
				target: process.env.KERN_API_ORIGIN ?? "http://localhost:8080",
				changeOrigin: false,
			},
		},
	},
});
