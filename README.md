# foto
Webapp til at tage fotos, samt tagge disse med holdnummer

# Byg
Kør `docker build -t fotoapp .` fra roden af mappen for at bygge Docker imaget

# Kør
Kør `docker run -p 80:80 -v /photos:/photos fotoapp`
Forklaring:
* `-p 80:80` Forward port 80 udefra ind til containeren. Kan erstattes af andet portnummer
* `-v /photos:/photos` Mount den lokale mappe `/photos` til containerens mappe `/photos` - Stien hvor billeder bliver gemt