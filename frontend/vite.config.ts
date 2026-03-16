import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

const frontendDir = fileURLToPath(new URL('.', import.meta.url));
const repoRoot = path.resolve(frontendDir, '..');

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, repoRoot, '');
  const serverPort = env.SERVER_PORT || '11953';
  const backendTarget = `http://localhost:${serverPort}`;

  return {
    plugins: [react()],
    server: {
      proxy: {
        '/api/voice/ws': {
          target: backendTarget,
          changeOrigin: true,
          ws: true
        },
        '/api': {
          target: backendTarget,
          changeOrigin: true
        },
        '/actuator': {
          target: backendTarget,
          changeOrigin: true
        }
      }
    }
  };
});
