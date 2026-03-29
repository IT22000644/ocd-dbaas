package harvester

import "fmt"

func buildCloudInit(p VMCreateParams, adminPw, replPw, exporterPw, luksKey string) string {
	dbName := p.DBName
	if dbName == "" {
		dbName = p.ID
	}

	port := p.Port
	if port == 0 {
		port = 5432
	}

	backupConfig := "# backups disabled"
	if p.BackupEnabled && p.S3Config != nil {
		backupConfig = fmt.Sprintf(
			"S3_ENDPOINT=%s\n      S3_BUCKET=%s\n      S3_REGION=%s\n      S3_SECRET_REF=%s",
			p.S3Config.Endpoint,
			p.S3Config.Bucket,
			p.S3Config.Region,
			p.S3Config.SecretRef,
		)
	}

	return fmt.Sprintf(`#cloud-config
package_update: true
packages:
  - postgresql
  - postgresql-contrib
  - jq
write_files:
  - path: /etc/dbaas/bootstrap.env
    permissions: "0600"
    content: |
      INSTANCE_ID=%s
      DB_NAME=%s
      DB_PORT=%d
      MASTER_USER=%s
      MASTER_PASSWORD=%s
      REPL_PASSWORD=%s
      EXPORTER_PASSWORD=%s
      MAX_CONNECTIONS=%d
      LUKS_KEY=%s
      %s
runcmd:
  - mkdir -p /var/lib/dbaas
  - chown postgres:postgres /var/lib/dbaas
  - systemctl enable postgresql || true
  - systemctl restart postgresql || systemctl start postgresql || true
final_message: "DBaaS bootstrap complete for %s"
`,
		p.ID,
		dbName,
		port,
		p.MasterUser,
		adminPw,
		replPw,
		exporterPw,
		p.MaxConnections,
		luksKey,
		backupConfig,
		p.ID,
	)
}
