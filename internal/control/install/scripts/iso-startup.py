#!/usr/bin/python3
import json, pathlib, shutil, subprocess, time, urllib.request, urllib.error
BASE = '__BASE__'
if shutil.which('ssh-keygen') and not pathlib.Path('/etc/ssh/ssh_host_ed25519_key').exists():
    subprocess.run(['ssh-keygen','-A'],check=True)
for attempt in range(12):
    try:
        with urllib.request.urlopen(BASE+'boot.json', timeout=5) as response:
            boot=json.load(response)
        phase=boot['phase']
        if phase in ('ready','cancelled','failed'):
            break
        if phase=='installed':
            subprocess.run(['systemctl','poweroff'],check=True)
            break
        body={'attempt':boot['attempt']}
        if phase=='initializing':
            # Clear shared identities before announcing that the template can be saved.
            pathlib.Path('/etc/machine-id').write_text('')
            try:
                pathlib.Path('/var/lib/dbus/machine-id').unlink()
            except FileNotFoundError:
                pass
            for key in pathlib.Path('/etc/ssh').glob('ssh_host_*'):
                key.unlink()
            body.update(phase='installed',message='Ubuntu 首次启动成功，等待关机保存镜像')
        elif phase=='verifying' and boot.get('boot_id'):
            body.update(phase='verified',boot_id=boot['boot_id'],message='Ubuntu 普通无盘启动回报成功')
        else:
            break
        request=urllib.request.Request(BASE+'report',data=json.dumps(body).encode(),headers={'Content-Type':'application/json'},method='POST')
        with urllib.request.urlopen(request,timeout=15) as response:
            response.read()
        if phase=='initializing':
            subprocess.run(['systemctl','poweroff'],check=True)
        break
    except urllib.error.HTTPError as error:
        if error.code in (401,403,404,410): break
        time.sleep(5)
    except (OSError,ValueError,subprocess.CalledProcessError):
        time.sleep(5)
