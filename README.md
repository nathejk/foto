# kamera
Webapp til at tage fotos, samt tagge disse med holdnummer

# Byg
Kør `docker build -t fotoapp .` fra roden af mappen for at bygge Docker imaget

# Kør
Kør `docker run -p 80:80 -v /photos:/photos fotoapp`
Forklaring:
* `-p 80:80` Forward port 80 udefra ind til containeren. Kan erstattes af andet portnummer
* `-v /photos:/photos` Mount den lokale mappe `/photos` til containerens mappe `/photos` - Stien hvor billeder bliver gemt

# Webhook
Når et billede er gemt, kan appen annoncere det ved at POSTe JSON til en URL:

```json
{
  "teamNumber": "42",
  "type": "start",
  "attention": false,
  "imageUrl": "https://foto.nathejk.dk/photos/2026/start/Team-42_1.jpg",
  "createdAt": "2026-09-06T15:24:00.000+00:00"
}
```

Konfigureres via environment variabler (eller `appsettings.json`):
* `WebhookUrl` - URL der kaldes. Tom/uangivet = webhook slået fra
* `WebhookSecret` - sendes med som `X-Webhook-Secret` header. Tom/uangivet = ingen header

```
docker run -p 80:80 -v /photos:/photos \
  -e WebhookUrl=https://example.com/hooks/foto \
  -e WebhookSecret=hemmeligt \
  fotoapp
```

Et kald der fejler stopper ikke uploadet - billedet er allerede gemt. I stedet
logges hele payloaden som en JSON-linje i `{PhotoPath}/webhook-failed.jsonl`, så
den kan sendes igen senere uden at miste data:

```
while read -r line; do
  curl -fsS -X POST -H 'content-type: application/json' \
    -H "X-Webhook-Secret: $WebhookSecret" -d "$line" "$WebhookUrl"
done < /photos/webhook-failed.jsonl
```
