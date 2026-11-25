export interface PreviewPage {
  id: string
  label: string
  html: string
}

// page and theme live in the address too (#detail/dark), so a view can be linked
const SCRIPT = `
const form = document.querySelector('.preview-bar')
function show() {
  const data = new FormData(form)
  document.documentElement.className = data.get('theme')
  for (const screen of document.querySelectorAll('[data-screen]'))
    screen.hidden = screen.dataset.screen !== data.get('screen')
  history.replaceState(null, '', '#' + data.get('screen') + '/' + data.get('theme'))
}
function pick(name, value) {
  const input = form.querySelector('input[name=' + name + '][value="' + value + '"]')
  if (input)
    input.checked = true
  return input
}
const [screen, theme] = location.hash.slice(1).split('/')
pick('screen', screen)
pick('theme', theme)
show()
form.addEventListener('change', show)
document.addEventListener('click', (event) => {
  const link = event.target.closest('a[href^="#"]')
  if (!link || !pick('screen', link.getAttribute('href').slice(1)))
    return
  event.preventDefault()
  show()
  window.scrollTo(0, 0)
})
`

export function previewPage(css: string, pages: readonly PreviewPage[]): string {
  const tabs = pages
    .map(({ id, label }, i) => `<label><input type="radio" name="screen" value="${id}"${i ? '' : ' checked'}>${label}</label>`)
    .join('')
  const screens = pages
    .map(({ id, html }, i) => `<div data-screen="${id}"${i ? ' hidden' : ''}>${html}</div>`)
    .join('\n')
  return `<!doctype html>
<html lang="en" class="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Cadence dashboard: design preview</title>
<style>${css}</style>
</head>
<body>
<form class="preview-bar" aria-label="Preview">
<strong>Cadence design preview</strong>
<fieldset><legend>Page</legend>${tabs}</fieldset>
<fieldset><legend>Theme</legend><label><input type="radio" name="theme" value="light" checked>Light</label><label><input type="radio" name="theme" value="dark">Dark</label></fieldset>
<small>Sample data. Resize the window for tablet and phone widths.</small>
</form>
${screens}
<script>${SCRIPT}</script>
</body>
</html>
`
}
