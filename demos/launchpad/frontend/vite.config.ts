import {defineConfig} from 'vite';
import react from '@vitejs/plugin-react';

// In development, /api goes to the stack's CloudFront domain: LAUNCHPAD_API=https://dxxxx.cloudfront.net npm run dev
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: process.env.LAUNCHPAD_API
      ? {'/api': {target: process.env.LAUNCHPAD_API, changeOrigin: true, secure: true}}
      : undefined,
  },
});
