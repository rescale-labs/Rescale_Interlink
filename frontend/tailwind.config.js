/** @type {import('tailwindcss').Config} */
export default {
  // v4.5.3: Disable automatic dark mode to prevent unreadable text
  // Dark mode was partially implemented but incomplete, causing contrast issues.
  // Setting to 'class' mode means dark variants only apply when <html class="dark">
  // is present - since we never add this class, dark mode is effectively disabled.
  darkMode: 'class',
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        // Rescale brand colors
        rescale: {
          blue: '#007ACC',
          'blue-dark': '#005A9E',
          'blue-light': '#3399DD',
        },
        // Status colors
        status: {
          success: '#4CAF50',
          warning: '#FF9800',
          error: '#F44336',
          info: '#2196F3',
        },
      },
      fontFamily: {
        sans: ['Inter', 'system-ui', '-apple-system', 'sans-serif'],
        mono: ['JetBrains Mono', 'Menlo', 'Monaco', 'monospace'],
      },
    },
  },
  plugins: [],
}
