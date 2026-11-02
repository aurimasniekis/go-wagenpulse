import { Box, useComputedColorScheme } from '@mantine/core'
import { BarChart, LineChart, ScatterChart } from 'echarts/charts'
import {
  DataZoomComponent,
  GridComponent,
  LegendComponent,
  MarkAreaComponent,
  TooltipComponent,
} from 'echarts/components'
import * as echarts from 'echarts/core'
import { SVGRenderer } from 'echarts/renderers'
import { useEffect, useRef } from 'react'
import { dark, light, type Palette } from '@/theme/tokens'

echarts.use([
  LineChart,
  BarChart,
  ScatterChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  DataZoomComponent,
  MarkAreaComponent,
  SVGRenderer,
])

export type EChartsOption = echarts.EChartsCoreOption

/** Base styling for charts in the current colour scheme. */
export function chartBase(p: Palette): EChartsOption {
  return {
    color: p.series,
    backgroundColor: 'transparent',
    textStyle: { color: p.textMuted, fontFamily: 'Inter Variable, Inter, system-ui, sans-serif', fontSize: 11 },
    grid: { left: 56, right: 24, top: 28, bottom: 36, containLabel: false },
    tooltip: {
      trigger: 'axis',
      backgroundColor: p.surface,
      borderColor: p.border,
      textStyle: { color: p.text, fontSize: 12 },
      confine: true,
    },
    xAxis: {
      type: 'time',
      axisLine: { lineStyle: { color: p.chartGrid } },
      axisLabel: { color: p.chartAxis, hideOverlap: true },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      scale: true,
      axisLabel: { color: p.chartAxis },
      splitLine: { lineStyle: { color: p.chartGrid } },
    },
  }
}

export function usePalette(): Palette {
  return useComputedColorScheme('light') === 'dark' ? dark : light
}

/** ECharts wrapper (SVG renderer): resizes with its box and replaces the option on change. */
export function EChart({
  option,
  height = 280,
  label,
  testId,
}: {
  option: EChartsOption
  height?: number
  label: string
  testId?: string
}) {
  const el = useRef<HTMLDivElement>(null)
  const chart = useRef<echarts.ECharts | null>(null)
  useEffect(() => {
    if (!el.current) return
    const c = echarts.init(el.current, undefined, { renderer: 'svg' })
    chart.current = c
    const ro = new ResizeObserver(() => {
      c.resize()
    })
    ro.observe(el.current)
    return () => {
      ro.disconnect()
      c.dispose()
      chart.current = null
    }
  }, [])
  useEffect(() => {
    chart.current?.setOption(option, { notMerge: true })
  }, [option])
  return <Box ref={el} h={height} w="100%" role="img" aria-label={label} data-testid={testId} />
}
