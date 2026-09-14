import type { MsgServerPres, MsgServerPresenceWhat } from '../../src';

const revoked: MsgServerPres = {
  topic: 'grp_42',
  src: 'grp_42',
  what: 'group_access_revoked',
};

const disbanded: MsgServerPresenceWhat = 'group_disbanded';
const futureCompatible: MsgServerPresenceWhat = 'future_group_event';

void revoked;
void disbanded;
void futureCompatible;
