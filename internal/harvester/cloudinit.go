package harvester

import "fmt"

func buildCloudInit(p VMCreateParams, adminPw, replPw, exporterPw, luksKey string) string {
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

	vmUserBlock := ""
	if p.VMPassword != "" {
		vmUserBlock = fmt.Sprintf(`password: %s
chpasswd:
  expire: false
ssh_pwauth: true
`, p.VMPassword)
	}

	return fmt.Sprintf(`#cloud-config
%spackage_update: true
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
  - path: /etc/netplan/60-vpc-net.yaml
    permissions: "0600"
    content: |
      network:
        version: 2
        ethernets:
          enp2s0:
            dhcp4: true
            dhcp4-overrides:
              use-routes: true
              route-metric: 200
  - path: /etc/dbaas/bootstrap.sh
    permissions: "0700"
    content: |
      #!/bin/bash
      set -euo pipefail
      source /etc/dbaas/bootstrap.env

      PG_VER=$(pg_lsclusters -h | awk '{print $1}' | head -1)
      PG_CONF="/etc/postgresql/${PG_VER}/main"

      # Listen on all interfaces and set the port
      sed -i "s/^#\?listen_addresses.*/listen_addresses = '*'/" "${PG_CONF}/postgresql.conf"
      sed -i "s/^#\?port.*/port = ${DB_PORT}/" "${PG_CONF}/postgresql.conf"
      sed -i "s/^#\?max_connections.*/max_connections = ${MAX_CONNECTIONS}/" "${PG_CONF}/postgresql.conf"

      # Allow remote connections with scram-sha-256
      echo "host all all 0.0.0.0/0 scram-sha-256" >> "${PG_CONF}/pg_hba.conf"
      echo "host replication all 0.0.0.0/0 scram-sha-256" >> "${PG_CONF}/pg_hba.conf"

      systemctl restart postgresql

      # Create admin user and database
      sudo -u postgres psql -p "${DB_PORT}" <<EOSQL
      DO \$\$
      BEGIN
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '${MASTER_USER}') THEN
          CREATE ROLE ${MASTER_USER} LOGIN SUPERUSER PASSWORD '${MASTER_PASSWORD}';
        END IF;
      END \$\$;
      SELECT 'CREATE DATABASE ${DB_NAME} OWNER ${MASTER_USER}'
        WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '${DB_NAME}')\gexec
      EOSQL
runcmd:
  - netplan apply
  - mkdir -p /var/lib/dbaas
  - chown postgres:postgres /var/lib/dbaas
  - /etc/dbaas/bootstrap.sh
final_message: "DBaaS bootstrap complete for %s"
`,
		vmUserBlock,
		p.ID,
		p.DBName,
		p.Port,
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
