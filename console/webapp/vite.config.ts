import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const envDir = '../..'
  const env = loadEnv(mode, envDir, '')
  const webappPort = Number.parseInt(env.CONSOLE_WEBAPP_PORT || '', 10) || 5173
  const consoleApiPort = env.CONSOLE_API_PORT || '8090'
  const consoleApiUrl = env.CONSOLE_API_URL || ''

  return {
    envDir,
    plugins: [react()],
    define: {
      'import.meta.env.VITE_CONSOLE_API_URL': JSON.stringify(consoleApiUrl),
      'import.meta.env.VITE_CONSOLE_API_PORT': JSON.stringify(consoleApiPort),
    },
    server: {
      port: webappPort,
      proxy: {
        '/console/api': {
          target: `http://127.0.0.1:${consoleApiPort}`,
          changeOrigin: true,
        },
      },
      allowedHosts: [
        'console.eigenflux.ai'
      ]
    },
  }
})
