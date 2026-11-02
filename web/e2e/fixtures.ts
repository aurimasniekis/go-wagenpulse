/**
 * Synthetic test data. The VIN is invented (not a real vehicle) and every value is made up. Signal keys are public
 * identifiers from the bundled EU Data Act dictionary; one key is deliberately absent from the dictionary.
 */
export const SYNTHETIC_VIN = 'XXXZZZ00ZXX000001'

export const signals = {
  soc: 'f89ed652-d104-3fa6-b7e2-ab7543309e7b', // hv_soc, %
  range: '153e8c40-4c6c-3c17-a11b-0ecc35d55b81', // cruising_range_combined, km
  charging: '9da735bb-c5d5-39f8-bf53-0fa2a367aa8f', // charging_state, String
  mileage: '41c0805c-43e5-313e-9dfb-356cb8d20f7c', // mileage, unsignedInt
  unknown: '00000000-0000-3000-8000-00000000e2e0', // not in any dictionary
}

/** A continuous delivery (vwdataact.continuous.v1 shape) with `points` samples per signal, 15 minutes apart. */
export function syntheticDelivery(points = 24, end = Date.now()): string {
  const data: { key: string; dataFieldName: string; value: string | null; timestampUtc: string }[] = []
  for (let i = 0; i < points; i++) {
    const t = new Date(end - (points - 1 - i) * 15 * 60_000).toISOString()
    const soc = 80 - i * 2
    data.push({ key: signals.soc, dataFieldName: 'hv_soc', value: String(soc), timestampUtc: t })
    data.push({ key: signals.range, dataFieldName: 'cruising_range_combined', value: String(soc * 4), timestampUtc: t })
    data.push({
      key: signals.charging,
      dataFieldName: 'charging_state',
      value: i % 8 < 4 ? 'notReadyForCharging' : 'charging',
      timestampUtc: t,
    })
    data.push({ key: signals.mileage, dataFieldName: 'mileage', value: String(12000 + i * 3), timestampUtc: t })
  }
  data.push({
    key: signals.unknown,
    dataFieldName: 'e2e_unknown_signal',
    value: '42',
    timestampUtc: new Date(end).toISOString(),
  })
  return JSON.stringify({ vin: SYNTHETIC_VIN, user_id: 'synthetic-user', Data: data })
}
