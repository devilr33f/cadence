const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB']

// three figures at most: 12.4 KiB, 411 MiB, 1.94 GiB
function figures(value: number): string {
  if (value >= 100 || Number.isInteger(value))
    return String(Math.round(value))
  return String(Number(value.toPrecision(3)))
}

export function bytesText(bytes: number): string {
  let value = Math.max(0, bytes)
  let unit = 0
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit++
  }
  // 1023.6 KiB rounds to 1024: one step up reads better
  if (Math.round(value) >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit++
  }
  return `${figures(value)} ${BYTE_UNITS[unit]}`
}

// a share (1 = 100%): whole percents, tenths under ten
export function percentText(share: number): string {
  const percent = share * 100
  return percent >= 10 || percent === 0
    ? `${Math.round(percent)}%`
    : `${Number(percent.toFixed(1))}%`
}

export function temperatureText(celsius: number): string {
  return `${celsius.toFixed(1)} °C`
}
