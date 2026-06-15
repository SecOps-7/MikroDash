async function fetchInterfaces(ros) {
  const items = await ros.write('/interface/print', ['=.proplist=.id,name,type,running,disabled']);
  return (items || []).map(i => ({ id: i['.id'], name: i.name, type: i.type, running: i.running, disabled: i.disabled }));
}
module.exports = { fetchInterfaces };
