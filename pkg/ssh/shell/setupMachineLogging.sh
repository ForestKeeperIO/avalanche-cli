#!/usr/bin/env bash
#!/usr/bin/env bash
{{if .IsE2E }}
#name:TASK [disable systemctl]
sudo cp -vf /usr/bin/true /usr/local/sbin/systemctl
{{end}}
#name:TASK [add repository]
mkdir -p /etc/apt/keyrings/
curl -s https://apt.grafana.com/gpg.key | sudo apt-key add -
echo "deb [signed-by=/etc/apt/keyrings/grafana.gpg] https://apt.grafana.com stable main" | tee /etc/apt/sources.list.d/grafana.list
sudo apt-get -y -o DPkg::Lock::Timeout=120 update
#name:TASK [install promtail]
sudo apt-get -y -o DPkg::Lock::Timeout=120 install promtail

