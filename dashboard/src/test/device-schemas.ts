import type { JsonSchema } from '@/components/forms/json-schema'

// capability schemas as the device declares them (device/internal/**), copied verbatim so the renderer meets the
// shapes it will be wired to
function parse(source: string): JsonSchema {
  return JSON.parse(source) as JsonSchema
}

const idSchema = `{"type":"string","pattern":"^[a-z0-9]{1,16}$"}`
const timeSchema = `{"type":"string","pattern":"^([01][0-9]|2[0-3]):[0-5][0-9]$"}`
const daysSchema = `{"type":"array","items":{"enum":["mon","tue","wed","thu","fri","sat","sun"]},"minItems":1,"maxItems":7,`
  + `"uniqueItems":true}`
const dateSchema = `{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$"}`
const sessionIDSchema = `{"type":"string","minLength":1,"maxLength":63}`
const levelSchema = `{"type":"integer","minimum":1,"maximum":100,"default":50}`
const secondsSchema = `{"type":"integer","minimum":1,"maximum":86400}`
const webURLSchema = `{"type":"string","pattern":"^[Hh][Tt][Tt][Pp][Ss]?://.","maxLength":2047}`
const telemetryLevel = `{"enum":["debug","info","warn","error"]}`

// alarms (device/internal/alarms/schemas.go)
export const ALARMS_CONFIG = parse(`{"type":"object","properties":{"timeZone":{"type":"string","minLength":1,"maxLength":64}},`
  + `"required":["timeZone"],"additionalProperties":false}`)
export const SET_ALARM = parse(`{"type":"object","properties":{"sessionId":${sessionIDSchema},"id":${idSchema},"time":${
  timeSchema},"days":${daysSchema},"date":${dateSchema},"level":${levelSchema}},`
  + `"required":["sessionId","id","time"],"not":{"required":["days","date"]},"additionalProperties":false}`)
export const SET_TIMER = parse(`{"type":"object","properties":{"sessionId":${sessionIDSchema},"id":${idSchema},"seconds":${
  secondsSchema}},"required":["sessionId","id","seconds"],"additionalProperties":false}`)
export const CANCEL_ALARM = parse(`{"type":"object","properties":{"sessionId":${sessionIDSchema},"id":${idSchema},"time":${
  timeSchema},"all":{"const":true}},"required":["sessionId"],`
  + `"oneOf":[{"required":["id"]},{"required":["time"]},{"required":["all"]}],"additionalProperties":false}`)
// the days list on its own, as a command's object would carry it
export const ALARM_DAYS = parse(`{"type":"object","properties":{"days":${daysSchema}},"additionalProperties":false}`)

// telemetry (device/internal/telemetry/schemas.go)
export const LOGS_CONFIG = parse(`{"type":"object","properties":{"sources":{"type":"object","maxProperties":64,`
  + `"patternProperties":{"^[a-z][a-z0-9-]{0,31}$":${telemetryLevel}},"additionalProperties":false},`
  + `"kmsg":{"type":"boolean"},"thirdParty":{"type":"boolean"},"traces":{"type":"boolean"}},"additionalProperties":false}`)
export const SYSTEM_METRICS_CONFIG = parse(`{"type":"object","properties":{"intervalS":{"type":"integer","minimum":10,"maximum":300,`
  + `"default":30}},"additionalProperties":false}`)

// media and audio (device/internal/media, soloist, volume)
export const PLAYER_CONFIG = parse(`{"type":"object","properties":{"duckMode":{"enum":["duck","muffle"]}},"additionalProperties":false}`)
export const SEEK = parse(`{"type":"object","properties":{"positionMs":{"type":"integer","minimum":0}},"required":["positionMs"],"additionalProperties":false}`)
export const NO_ARGS = parse(`{"type":"object","additionalProperties":false}`)
// pkg/capschema: a speaker's delay trim alone, a screen's with whose volume the level is
const delayMs = `"delayMs":{"type":"integer","minimum":-1000,"maximum":1000}`
export const OUTPUT_CONFIG = parse(`{"type":"object","properties":{${delayMs}},"additionalProperties":false}`)
export const OUTPUT_SCREEN_CONFIG = parse(`{"type":"object","properties":{${delayMs},`
  + `"volume":{"type":"object","properties":{"mode":{"enum":["software","system"]}},"additionalProperties":false}`
  + `},"additionalProperties":false}`)
export const SOLOIST_CONFIG = parse(`{"type":"object","properties":{"enabled":{"type":"boolean"},`
  + `"deviceName":{"type":"string","minLength":1,"maxLength":32,"pattern":"^[A-Za-z0-9._-]([A-Za-z0-9._ -]*[A-Za-z0-9._-])?$"}},`
  + `"additionalProperties":false}`)
export const EQ_CONFIG = parse(`{"type":"object","properties":{"preampDb":{"type":"number","minimum":-12,"maximum":12},`
  + `"bands":{"type":"array","maxItems":10,"items":{"type":"object","properties":{`
  + `"type":{"enum":["peaking","lowShelf","highShelf","lowPass","highPass"]},`
  + `"freqHz":{"type":"number","minimum":20,"maximum":20000},`
  + `"gainDb":{"type":"number","minimum":-12,"maximum":12},`
  + `"q":{"type":"number","minimum":0.1,"maximum":10}`
  + `},"required":["type","freqHz","gainDb","q"],"additionalProperties":false}}},"additionalProperties":false}`)
export const URL_PLAY = parse(`{"type":"object","properties":{`
  + `"url":${webURLSchema},`
  + `"mode":{"enum":["session","overlay"]},`
  + `"metadata":{"type":"object","properties":{`
  + `"title":{"type":"string"},"artist":{"type":"string"},"album":{"type":"string"},"artworkUrl":${webURLSchema
  }},"additionalProperties":false},`
  + `"startMs":{"type":"integer","minimum":0}`
  + `},"required":["url"],"additionalProperties":false}`)
export const LIGHTS_CONFIG = parse(`{"type":"object","properties":{"lights":{"type":"boolean"},"clock":{"type":"boolean"}},`
  + `"additionalProperties":false}`)
