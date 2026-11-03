# Опциональный GitLab Runner в WSL

Нужен только если собственный CI снова понадобится чаще. Регистрация не выполняется автоматически и токен не хранится в проекте.

## Границы безопасности

- Используйте отдельный WSL-дистрибутив или хотя бы отдельного пользователя `gitlab-runner`.
- Не подключайте `/var/run/docker.sock`, production `.env`, named volumes и каталоги backup к runner.
- Текущие GitLab jobs выполняются прямо в Linux: Docker и Docker-in-Docker не нужны.
- Runner должен видеть только изолированный checkout репозитория, который создаёт CI.

## 1. Установите зависимости в Ubuntu/WSL

Нужны Go 1.27.3 или совместимая версия, PowerShell 7, Git, GCC, wget и CA certificates.

```bash
sudo apt-get update
sudo apt-get install -y git wget ca-certificates gcc curl
wget -q https://github.com/PowerShell/PowerShell/releases/download/v7.6.0/powershell_7.6.0-1.deb_amd64.deb -O /tmp/powershell.deb
sudo apt-get install -y /tmp/powershell.deb
```

Установите Go через https://go.dev/dl/ или пакетный менеджер. Проверка:

```bash
go version
pwsh --version
gcc --version
```

## 2. Установите runner

Для текущего x86_64 WSL:

```bash
curl -L --output /tmp/gitlab-runner https://gitlab-runner-downloads.s3.amazonaws.com/latest/binaries/gitlab-runner-linux-amd64
sudo install -m 0755 /tmp/gitlab-runner /usr/local/bin/gitlab-runner
sudo useradd --comment "GitLab Runner" --create-home gitlab-runner --shell /bin/bash
```

Если пользователь уже существует, `useradd` можно пропустить.

## 3. Зарегистрируйте runner через GitLab UI

В приватном проекте: **Settings → CI/CD → Runners → New runner**. Выберите Linux и shell executor. Полученный registration token вводите только локально, не в чат и не в репозиторий.

```bash
sudo gitlab-runner register
```

Укажите:

- GitLab URL: `https://gitlab.com/`
- Executor: `shell`
- Working directory: `/home/gitlab-runner`

Затем:

```bash
sudo gitlab-runner install --user gitlab-runner --working-directory /home/gitlab-runner
sudo gitlab-runner start
sudo gitlab-runner verify
```

WSL должен работать во время pipeline. Sleep/выключение ПК останавливает выполнение.

## 4. Проверка и экономия shared minutes

- Сначала запустите pipeline вручную через GitLab **Build → Pipelines → Run pipeline**.
- Не включайте одновременно self-hosted и shared runners для одной нагрузки.
- После успешного self-hosted запуска можно отключить shared runners в настройках проекта.
- Если runner больше не нужен, остановите его: `sudo gitlab-runner stop`.

Локальная проверка разработчика остаётся прежней:

```powershell
pwsh -NoLogo -NoProfile -File scripts/verify.ps1 -WithVolumeRestore
```
