const ipaddr = require('ipaddr.js');
/**
 * Split `addr/prefix`, defaulting a missing or unreadable prefix to the whole
 * address — /32 for v4, /128 for v6 — which is what a bare address means
 * everywhere in RouterOS.
 *
 * It used to hand `parseInt(undefined, 10)` straight to ipaddr.js, which is NaN.
 * `matchCIDR` loops `while (cidrBits > 0)`, NaN fails that immediately, and the
 * function returns TRUE. So a spec with no prefix matched EVERY address of the
 * same family, and the same rule written two ways disagreed:
 *
 *   isInCidrs('8.8.8.8', ['10.0.0.5'])    -> true
 *   isInCidrs('8.8.8.8', ['10.0.0.0/8'])  -> false
 *
 * fwGuard is where that was reachable. Blocking one abusive host is an ordinary
 * edit, and whether it raised a lockout warning depended only on whether the
 * operator typed the `/32`. A guard that cries wolf on the ordinary case teaches
 * people to click through the warning that mattered, which is the whole thing
 * that module exists to avoid.
 *
 * An over-long mask (`/33`) still throws and is still caught as `false`: it is
 * not a prefix this address could have, so it is not a spec we can honour.
 */
function parseCIDR(cidr){
  const parts = String(cidr).split('/');
  const addr  = ipaddr.parse(parts[0]);
  const bits  = parseInt(parts[1], 10);
  const full  = addr.kind() === 'ipv6' ? 128 : 32;
  return [addr, Number.isNaN(bits) ? full : bits];
}
function isInCidrs(ip, cidrs){
  if(!ip) return false;
  let obj; try{ obj=ipaddr.parse(ip);}catch{return false;}
  return (cidrs||[]).some(c=>{ try{ const [n,p]=parseCIDR(c); return obj.match([n,p]); }catch{return false;} });
}
function extractAddress(value){
  const raw = String(value || '').trim();
  if(!raw) return '';
  const bracketed = raw.match(/^\[([^\]]+)\](?::\d+(?:\/.*)?)?$/);
  if(bracketed) return bracketed[1];
  if(ipaddr.isValid(raw)) return raw;

  const slash = raw.indexOf('/');
  const withoutCidr = slash === -1 ? raw : raw.slice(0, slash);
  if(ipaddr.isValid(withoutCidr)) return withoutCidr;

  const lastColon = raw.lastIndexOf(':');
  if(lastColon > 0){
    const host = raw.slice(0, lastColon);
    const port = raw.slice(lastColon + 1).replace(/\/.*$/, '');
    if(/^\d+$/.test(port) && (ipaddr.isValid(host) || host.indexOf(':') === -1)) return host;
  }

  return withoutCidr;
}
function isValidIp(ip){
  return !!ip && ipaddr.isValid(ip);
}
module.exports = { isInCidrs, extractAddress, isValidIp };
