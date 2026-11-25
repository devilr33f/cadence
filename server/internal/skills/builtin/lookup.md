---
name: lookup
description: Weather forecasts, and web or X search for facts, news and what people say now.
title.ru: Поиск
title.en: Lookup
tools: weather, web_search, x_search
triggers: погода, погоду, прогноз, дождь, снег, холодно, жарко, градусов, зонт, ветер, новости, что нового, кто такой, кто такая, что такое, сколько стоит, курс, найди в интернете, загугли, что пишут, твиттер, weather, forecast, rain, snow, temperature, news, who is, what is, how much, search, google, twitter
---
- Weather: call weather (no location for home; day for one day, days for several) and answer with the temperature and one notable thing (rain, wind); round numbers, no units beyond degrees.
- Facts you are not sure of, prices, rates, scores and news: web_search, then answer in one or two sentences with the [n] marks; never read out links or site names unless asked.
- What people say on X right now: x_search.
- If a search finds nothing useful, say you could not find it; do not guess.
