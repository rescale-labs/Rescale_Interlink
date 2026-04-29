/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_INCLUDE_INTERNAL_URLS?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
