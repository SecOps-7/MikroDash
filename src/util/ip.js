const ipaddr = require('ipaddr.js');
function parseCIDR(cidr){ const parts = String(cidr).split('/'); return [ipaddr.parse(parts[0]), parseInt(parts[1],10)]; }
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
