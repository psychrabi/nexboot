# -*- coding: utf-8 -*-
# Compatible with the Python 2.7 runtime on EL7 and Python 3 on EL8/9.
from __future__ import print_function
import glob, json, os, re, subprocess, sys
BASE = '__BASE__'
SERIAL = '__SERIAL__'
SIZE = __SIZE__

def report(phase, message):
    subprocess.check_call(['curl', '-fsS', '--max-time', '30', '-H', 'Content-Type: application/json', '--data', json.dumps({'attempt': __ATTEMPT__, 'phase': phase, 'message': message}), BASE+'report'])

def norm(value):
    return re.sub('[^a-z0-9]', '', value.lower())

try:
    subprocess.check_call(['udevadm', 'settle', '--timeout=30'])
    matches = []
    for block in glob.glob('/sys/class/block/*'):
        if os.path.exists(block+'/partition'):
            continue
        try:
            with open(block+'/device/vpd_pg80', 'rb') as f:
                vpd = f.read()
            serial = vpd[4:].decode('ascii').strip()
        except (IOError, UnicodeError):
            continue
        if norm(serial) != norm(SERIAL):
            continue
        with open(block+'/size') as f:
            size = int(f.read().strip()) * 512
        with open(block+'/ro') as f:
            readonly = int(f.read().strip())
        device = os.path.realpath(block+'/device')
        name = os.path.basename(block)
        if not re.match('^sd[a-z]+$', name) or not re.search('/session[0-9]+/', device) or not device.endswith(':0') or readonly or size != SIZE:
            raise ValueError('Matching installation serial is not the expected writable iSCSI LUN 0 and size')
        matches.append(name)
    if len(matches) != 1:
        raise ValueError('Installation disk identity is missing or ambiguous; local disks were not changed')
    disk = matches[0]
    # No disk command is handed to Anaconda until identity, transport, LUN and size agree.
    with open('/tmp/nd-partitions.ks', 'w') as f:
        f.write('ignoredisk --only-use={0}\nclearpart --all --initlabel --drives={0}\nbootloader --boot-drive={0} --append="rd.iscsi.firmware=1 rd.iscsi.ibft=1 rd.neednet=1 ip=ibft"\npart /boot/efi --fstype=efi --size=512 --ondisk={0}\npart /boot --fstype=ext4 --size=1024 --ondisk={0}\npart / --fstype=ext4 --size=4096 --grow --ondisk={0}\n'.format(disk))
    report('installing', u'已确认服务端安装盘，正在从本地 DVD 安装 RHEL 系系统')
except Exception as error:
    print(str(error), file=sys.stderr)
    try:
        report('failed', u'请查看安装日志并确认服务端安装盘：无法唯一确认可写的 iSCSI 系统盘，本地磁盘未被格式化')
    except Exception:
        pass
    sys.exit(1)
