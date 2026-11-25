---
name: home
description: Lights, switches, climate and other home devices through Home Assistant: turn on and off, set, check state.
title.ru: Дом
title.en: Home
tools: ha_control, ha_search, ha_inspect
triggers: свет, лампа, лампу, люстра, люстру, торшер, розетка, розетку, кондиционер, кондей, обогреватель, отопление, батарея, температура, термостат, увлажнитель, очиститель, вентилятор, шторы, жалюзи, пылесос, чайник, бойлер, замок, ворота, включи, выключи, переключи, сделай теплее, сделай холоднее, ярче, темнее, light, lamp, switch, socket, climate, air conditioner, heater, thermostat, fan, curtains, vacuum, turn on, turn off, brighter, dimmer
---
- Every change goes through ha_control, follow-ups included. An ok answer without pending confirms the change; never reply "включил" or "выключил" without that confirmation. Never reply "включил" or "выключаю" without that call.
- If ha_control answers ok with `pending: true`, the device accepted the command but has not confirmed its new state yet. Say it is in progress ("Выключаю кондиционер"), never "выключен" or "не получилось", and do not retry. `state` is what it still reads and `expected` is the target; the final outcome continues onto the live card and into the conversation history.
- A later event turn may record that pending command's final confirmation or `no_effect`. Use that newer outcome instead of the earlier pending result; it is not spoken automatically.
- target is the device's name as the user said it ("свет в спальне", "кондиционер"). With no room named, the home note's favorites and this device's room come first; if several devices still match, ask which one.
- A bare "выключи" / "включи" / "сделай потеплее" means the device the conversation was just about.
- "Ярче" / "темнее": set_brightness relative to the state the home note or ha_search shows (about 20 points a step). "Теплее" / "холоднее" on a climate: set_temperature 1 degree from its current target.
- Questions about state ("горит ли свет", "какая температура в детской") go to ha_search, never ha_control.
- For anything other than on, off, brightness, colour temperature, temperature and hvac mode: ha_inspect for the services, then ha_control call_service.
- A lock, gate or alarm may be refused by policy: then say it plainly and do not retry.
- Reply after confirmed success in a few words ("Готово", "Включила свет в спальне"), never with entity ids.
