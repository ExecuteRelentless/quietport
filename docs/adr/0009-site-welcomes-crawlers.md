# 0009 The site welcomes search engines and AI crawlers

**Context.** The site had no robots.txt of its own, so `/robots.txt` fell through to Headscale, whose built-in file
disallows everything. quietport.app was closed to every search engine and AI crawler from the day the domain went
live. The project wants people to find it, including through AI assistants.

**Decision.** The site serves its own `robots.txt`: everything allowed except invite links (`/j/`) and downloads
(`/dl/`), the main AI crawlers named so the permission is explicit, and the Content Signals line set to
`search=yes, ai-input=yes, ai-train=yes`. A crawler that matches a named group ignores the `*` group, so the named
group repeats the same Disallow lines. The site also serves `sitemap.xml`, `llms.txt` (the llmstxt.org format) and an
IndexNow key file. The home page carries a canonical link, Open Graph and Twitter tags with a share card, and
SoftwareApplication JSON-LD.

**Consequences.** AI companies may train on the site text. That is intended, because the public site holds only
product text and member files and names never reach it. `ai-train` is the setting to change if that stops being true.
`llms.txt` has to change when a feature or a limit changes. After a site change, send the changed URLs to IndexNow,
which feeds Bing and the answer engines that read Bing. Google does not take IndexNow and reads `sitemap.xml` instead.
