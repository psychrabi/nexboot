# EL7 installer-only compatibility update; Python 2.7.
# Preserve the vendor module and adopt only a uniquely verified live iBFT session.
import glob as _nd_glob
import os as _nd_os

_nd_sys = '/sys'

def _nd_read(path):
    with open(path) as stream:
        return stream.read().strip()

def _nd_existing_session(owner, node, error):
    if str(error) != 'initiator reported error (15 - session exists)':
        return False
    try:
        matches = 0
        for path in _nd_glob.glob(_nd_sys + '/class/iscsi_session/session*'):
            if (_nd_read(path + '/state') != 'LOGGED_IN' or
                    _nd_read(path + '/targetname') != node.name or
                    _nd_read(path + '/ifacename') != node.iface or
                    not owner._initiator or
                    _nd_read(path + '/initiatorname') != owner._initiator):
                continue
            sid = _nd_os.path.basename(path)[len('session'):]
            connections = _nd_glob.glob(_nd_sys + '/class/iscsi_connection/connection' + sid + ':*')
            if len(connections) != 1:
                continue
            conn = connections[0]
            if (_nd_read(conn + '/address') == node.address and
                    _nd_read(conn + '/port') == str(node.port)):
                matches += 1
        return matches == 1
    except (IOError, OSError, AttributeError, ValueError):
        return False

def _nd_patch(source):
    marker = '''            except IOError as e:
                log.error("iSCSI: could not log into ibft %s at %s:%s through %s: %s",'''
    if source.count(marker) != 1:
        raise RuntimeError('NexBoot EL7 iSCSI update: unsupported vendor IBFT handler; cannot safely apply compatibility update')
    replacement = '''            except IOError as e:
                if _nd_existing_session(self, node, e):
                    log.info("NexBoot iSCSI IBFT: reused existing session for %s at %s:%s through %s", node.name, node.address, node.port, node.iface)
                    self.ibftNodes.append(node)
                    continue
                log.error("iSCSI: could not log into ibft %s at %s:%s through %s: %s",'''
    return source.replace(marker, replacement, 1)

# Load the original vendor file, never this updates overlay itself.
_nd_vendor = '/usr/lib/python2.7/site-packages/blivet/iscsi.py'
with open(_nd_vendor) as _nd_stream:
    _nd_source = _nd_patch(_nd_stream.read())
exec(compile(_nd_source, _nd_vendor, 'exec'))
