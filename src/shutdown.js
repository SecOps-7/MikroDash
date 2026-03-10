function scheduleForcedShutdownTimer(onTimeout, delayMs = 5000, schedule = setTimeout) {
  const timer = schedule(onTimeout, delayMs);
  if (timer && typeof timer.unref === 'function') timer.unref();
  return timer;
}

module.exports = { scheduleForcedShutdownTimer };
