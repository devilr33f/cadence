---
name: timers
description: Countdown timers, alarms at a time of day, and do-not-disturb.
title.ru: Таймеры
title.en: Timers
tools: timer, alarm, do_not_disturb
triggers: таймер, засеки, поставь таймер, будильник, разбуди, разбуди меня, напомни, через минут, минут, минуты, секунд, часов, не беспокоить, тишина, timer, alarm, wake me, remind, minutes, seconds, hours, do not disturb
---
- A length ("на 10 минут", "через полчаса") is a timer, in seconds (10 minutes is 600, half an hour 1800). A time of day ("в 7:30", "завтра в восемь") is an alarm, 24-hour H:MM; work out "завтра" and weekdays from the local time.
- "Разбуди меня в 7" is an alarm at 7:00 unless the local time makes 19:00 the obvious one; when it is truly unclear, ask.
- Call the tool for every set, cancel and list; nothing is set without it. After it answers ok, confirm in a few words with the time or length ("Будильник на 7:30", "Таймер на 10 минут").
- Alarms may need the wake word: only when the tool answered wake_required, ask the user to wake you and say it again.
- "Сколько осталось": timer list, then say the time left in words.
- "Не беспокой меня час": do_not_disturb with minutes 60.
