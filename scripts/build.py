import argparse
import os
import platform
import shutil
import subprocess
from pathlib import Path


def read_version(root: Path) -> str:
    version_file = root / 'VERSION'
    if version_file.is_file():
        value = version_file.read_text(encoding='utf-8').strip()
        if value:
            return value
    return '1.0.13+113'


def host_goos() -> str:
    system = platform.system().lower()
    if system == 'windows':
        return 'windows'
    if system == 'darwin':
        return 'darwin'
    return 'linux'


def host_goarch() -> str:
    machine = platform.machine().lower()
    if machine in {'arm64', 'aarch64'}:
        return 'arm64'
    return 'amd64'


def main() -> None:
    root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description='构建真果鉴网页版独立单文件服务端')
    parser.add_argument(
        '--os',
        dest='target_os',
        choices=['host', 'windows', 'linux', 'darwin', 'all'],
        default='host',
    )
    parser.add_argument('--arch', choices=['host', 'amd64', 'arm64'], default='host')
    parser.add_argument('--output-dir', type=Path, default=root / 'dist')
    options = parser.parse_args()

    go = shutil.which('go')
    if not go:
        raise SystemExit('请先安装 Go 1.24.1 或更新版本。')

    version = read_version(root)
    ldflags = f'-s -w -X duanjuapp/native/core.buildAllSources=true -X main.appVersion={version}'
    update_server = os.environ.get('UPDATE_SERVER_URL', '').strip()
    if update_server:
        ldflags += f' -X duanjuapp/native/core.nativeUpdateServer={update_server}'

    environment = os.environ.copy()
    environment.setdefault('GOPROXY', 'https://goproxy.cn,direct')
    environment.setdefault('GOSUMDB', 'off')
    environment['CGO_ENABLED'] = '0'

    if options.target_os == 'all':
        targets = [
            ('windows', 'amd64'),
            ('linux', 'amd64'),
            ('linux', 'arm64'),
            ('darwin', 'arm64'),
            ('darwin', 'amd64'),
        ]
    else:
        goos = host_goos() if options.target_os == 'host' else options.target_os
        goarch = host_goarch() if options.arch == 'host' else options.arch
        targets = [(goos, goarch)]

    output_dir = options.output_dir.resolve()
    output_dir.mkdir(parents=True, exist_ok=True)

    for goos, goarch in targets:
        suffix = '.exe' if goos == 'windows' else ''
        binary = output_dir / f'zhenguojian-web-{goos}-{goarch}{suffix}'
        build_env = environment.copy()
        build_env.update(GOOS=goos, GOARCH=goarch)
        print(f'Building {binary.name}', flush=True)
        subprocess.run(
            [go, 'build', '-trimpath', '-ldflags=' + ldflags, '-o', str(binary), './web'],
            cwd=root,
            env=build_env,
            check=True,
        )
        size_mb = binary.stat().st_size / (1024 * 1024)
        print(f'Built {binary} ({size_mb:.2f} MB)', flush=True)


if __name__ == '__main__':
    main()
