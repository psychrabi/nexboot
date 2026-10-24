# Exact CentOS 7 blivet _startIBFT, GPL-2.0-or-later, Red Hat Inc.
# Extracted from python-blivet DVD package; preserve vendor implementation.
class Vendor(object):
    def _startIBFT(self):
        if not flags.ibft:
            return

        try:
            found_nodes = libiscsi.discover_firmware()
        except Exception: # pylint: disable=broad-except
            log_exception_info(log.info, "iscsi: No IBFT info found.")
            # an exception here means there is no ibft firmware, just return
            return

        for node in found_nodes:
            try:
                node.login()
                log.info("iscsi IBFT: logged into %s at %s:%s through %s",
                    node.name, node.address, node.port, node.iface)
                self.ibftNodes.append(node)
            except IOError as e:
                log.error("iSCSI: could not log into ibft %s at %s:%s through %s: %s",
                         node.name, node.address, node.port, node.iface,
                         str(e))

        self.stabilize()

