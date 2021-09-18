#!/bin/bash

set -xe
cd go && make && cd -

for h in 1 2 3
do
   rsync -av ~/env.sh isucon${h}:/home/isucon/
   rsync -av ./go/ isucon${h}:/home/isucon/webapp/go/
   rsync -av ./sql/ isucon${h}:/home/isucon/webapp/sql/
   ssh isucon${h} sudo systemctl restart isucholar.go.service
done

rsync -av /etc/nginx/ isucon${h}:/tmp/etc/nginx/
for h in 1 2 3
do
   ssh isucon${h} sudo mkdir -p /tmp/etc/nginx 
   ssh isucon${h} sudo rsync -av /tmp/etc/nginx/ /etc/nginx/
   ssh isucon${h} sudo touch /var/log/nginx/access.log
   ssh isucon${h} sudo mv /var/log/nginx/access.log /var/log/nginx/access.log.$(date +%Y%m%d-%H%M%S)
   ssh isucon${h} sudo systemctl restart nginx
done

