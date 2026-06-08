FROM google/cloud-sdk:slim

COPY upload.sh /usr/local/bin/upload.sh
RUN chmod +x /usr/local/bin/upload.sh

ENTRYPOINT ["/usr/local/bin/upload.sh"]
