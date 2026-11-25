// writes design-preview/index.html: the real components and tokens, rendered to one self-contained file (css and the
// latin and cyrillic font files inlined, no requests); run with `pnpm preview:build` after a design change
import { readFileSync, writeFileSync } from 'node:fs'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import viteReact from '@vitejs/plugin-react'
import { createServer } from 'vite'
import { previewPage } from './page.ts'

const fonts = new URL('../node_modules/@fontsource-variable/', import.meta.url)

// the faces of src/styles/fonts.css (latin and cyrillic), each file inlined
function inlineFonts(css: string): string {
  return css.replace(/@font-face\s*\{[^}]*\}/g, (face) => {
    const file = /url\(["']?[^"')]*\/files\/([\w-]+\.woff2)["']?\)/.exec(face)?.[1]
    // the preview must stay self-contained: a face it cannot inline stops the build
    if (!file)
      throw new Error(`no font file to inline in ${face.slice(0, 120)}`)
    const pkg = file.startsWith('geist-mono')
      ? 'geist-mono'
      : 'geist'
    const data = readFileSync(new URL(`${pkg}/files/${file}`, fonts)).toString('base64')
    return face.replace(/url\([^)]*\)/, `url(data:font/woff2;base64,${data})`)
  })
}

async function build() {
  const vite = await createServer({
    root: fileURLToPath(new URL('..', import.meta.url)),
    configFile: false,
    logLevel: 'error',
    appType: 'custom',
    resolve: { tsconfigPaths: true },
    server: { middlewareMode: true, hmr: false, ws: false },
    plugins: [
      tailwindcss(),
      viteReact(),
    ],
  })
  try {
    const { renderScreens } = await vite.ssrLoadModule('/design-preview/render.tsx') as typeof import('./render')
    const pages = await renderScreens()
    const { default: css } = await vite.ssrLoadModule('/design-preview/preview.css?inline') as { default: string }
    const document = previewPage(inlineFonts(css), pages)
    writeFileSync(new URL('index.html', import.meta.url), document)
    process.stdout.write(`design-preview/index.html: ${pages.length} pages, ${Math.round(document.length / 1024)} KiB\n`)
  }
  finally {
    await vite.close()
  }
}

build().catch((error: unknown) => {
  console.error(error)
  process.exitCode = 1
})
