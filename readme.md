### Simple backuper

This software is a simple backup tool for all kind of purposes. It is written in go. 

The goal is to run a set of commands and upload a single file to a s3 compatible storage.

To use it you just need to add a config.yaml file like this one: 

```yaml
# You can use the variables below with ${} in your script and filepath_to_upload
#  BACKUP_ID
#  BACKUP_NAME
#  TEMP_DIR (without trailing slash at the end)
#  And all environment variables set

# Example of tar gzipping a file and uploading it to the backup:
#  tar -czf test.tar.gz test1.log test2/test2.log
jobs:
  - name: nextcloud-backup
    # every day at 12:00
    schedule: "0 12 * * *"
    script:
      - cd ${TEMP_DIR}
      - mysqldump -u ${MYSQL_USER} -p${MYSQL_PASSWORD} -h ${MYSQL_HOST} nextcloud > out.sql
      - tar -czf nextcloud.tar.gz out.sql
    filepath_to_upload: ${TEMP_DIR}/nextcloud.tar.gz

  - name: ghost-backup
    schedule: "0 12 * * *"
    script:
      - cd ${TEMP_DIR}
      - mysqldump -u ${MYSQL_USER} -p${MYSQL_PASSWORD} -h ${MYSQL_HOST} ghost > out.sql
      - tar -czf ghost.tar.gz out.sql
    filepath_to_upload: ${TEMP_DIR}/ghost.tar.gz
```

As you can see it's pretty simple. You can use all environment variables set in the docker container and the variables listed above.

The image use debian and by default add the following packages :
- default-mysql-client
- postgresql-client

The typicall usage will be to have your own image, in a project I have done that : 
```dockerfile
FROM alexisvisco/simple-backuper:0.0.3
ENV TZ="Europe/Paris"
COPY config.yml config.yml
```

You can imagine adding packets like rsync, ssh, etc... to your image to do more complex backup.

A docker run will just works, needed environment variables are : 

```.env 
S3_ENDPOINT=...
S3_REGION=eu-paris-1
S3_BUCKET=backups
S3_SECRET_KEY=...
S3_ACCESS_KEY=...
CONFIG_PATH=config.yml
S3_AUTO_CREATE_BUCKET=true
```
