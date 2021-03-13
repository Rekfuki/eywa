import React, { useState, useEffect } from 'react';
import PropTypes from 'prop-types';

import Chart from 'react-apexcharts';
import moment from 'moment';
import { useSnackbar } from 'notistack';
import {
  Card,
  CardContent,
  useTheme
} from '@material-ui/core';
import axios from 'src/utils/axios';
import {
  roundInterval
} from 'src/utils/time';
import { parseValue } from 'src/utils/numbers';

const RequestAndErrorRates = ({ functionId, endTime, range, width }) => {
  const theme = useTheme();
  const { enqueueSnackbar } = useSnackbar();
  const [chart, setChart] = useState(null);

  const minInterval = 5 * 1000;


  const getSeries = async () => {
    try {
      console.log("range: ", range)
      console.log("width: ", width);
      let step = roundInterval(range / width);
      step = step < minInterval ? minInterval : step;
      endTime = Math.floor(endTime / step) * step;
      const startTime = endTime - range;

      Promise.all([
        getAllRequests(startTime, endTime, step),
        getXXXRequests(`code=~"2.."`, startTime, endTime, step),
        getXXXRequests(`code=~"4.."`, startTime, endTime, step),
        getXXXRequests(`code=~"5.."`, startTime, endTime, step)
      ])
        .then(([allRequests, status2XX, status4XX, status5XX]) => {
          if (allRequests.length > 0) allRequests[0] = { ...allRequests[0], name: "Request Rate" }
          if (status2XX.length > 0) status2XX[0] = { ...status2XX[0], name: "2XX Success Rate" }
          if (status4XX.length > 0) status4XX[0] = { ...status4XX[0], name: "4XX Errors Rate" }
          if (status5XX.length > 0) status5XX[0] = { ...status5XX[0], name: "5XX Errors Rate" }
          const series = allRequests.concat(status2XX).concat(status4XX).concat(status5XX)

          const newChart = buildChart(series);
          addTimeAxis(newChart.options, width, endTime, range);

          setChart(newChart);
        })
    } catch (err) {
      console.error(err);
      enqueueSnackbar('Failed to get metrics', {
        variant: 'error'
      });
    }
  };

  const getAllRequests = async (startTime, endTime, step) => {
    const payload = {
      "type": "range",
      "series": ["gateway_function_invocation_total"],
      "label_matchers": `function_id="${functionId}"`,
      "group_by": "function_id",
      "query": `sum(rate(<<index .Series 0>>{<<.LabelMatchers>>}[${step}ms])) by(<<.GroupBy>>)`,
      "start": startTime / 1000,
      "end": endTime / 1000,
      "step": step / 1000
    }
    const response = await axios.post(`/eywa/api/metrics/query`, payload)
    return processMetrics(response.data.Data)
  };


  const getXXXRequests = async (codeLabel, startTime, endTime, step) => {
    const payload = {
      "type": "range",
      "series": ["gateway_function_invocation_total"],
      "label_matchers": `function_id="${functionId}",${codeLabel}`,
      "group_by": "function_id",
      "query": `sum(rate(<<index .Series 0>>{<<.LabelMatchers>>}[${step}ms])) by(<<.GroupBy>>)`,
      "start": startTime / 1000,
      "end": endTime / 1000,
      "step": step / 1000
    }
    const response = await axios.post(`/eywa/api/metrics/query`, payload)
    return processMetrics(response.data.Data)
  }


  const processMetrics = (data) => {
    return data.result.map(({ values }) => {
      const data = []
      for (let i = 0; i < values.length; i++) {
        data.push([values[i][0] * 1000, parseValue(values[i][1])])
      }
      return {
        data: data
      }
    });
  }

  useEffect(() => {
    getSeries();
  }, [range, endTime, width])

  const graphTimeFormat = (ticks, min, max) => {
    if (min && max && ticks) {
      const range = max - min;
      const secPerTick = range / ticks / 1000;
      // Need have 10 millisecond margin on the day range
      // As sometimes last 24 hour dashboard evaluates to more than 86400000
      const oneDay = 86400010;
      const oneYear = 31536000000;

      if (secPerTick <= 45) {
        return 'HH:mm:ss';
      }
      if (secPerTick <= 7200 || range <= oneDay) {
        return 'HH:mm';
      }
      if (secPerTick <= 80000) {
        return 'MM/DD HH:mm';
      }
      if (secPerTick <= 2419200 || range <= oneYear) {
        return 'MM/DD';
      }
      if (secPerTick <= 31536000) {
        return 'YYYY-MM';
      }
      return 'YYYY';
    }

    return 'HH:mm';
  };

  const addTimeAxis = (options, width, endTime, range) => {
    const ticks = width ? width / 100 : 2;

    const min = endTime - range;
    const max = endTime;


    options.xaxis = {
      type: "datetime",
      min: min,
      max: max,
      label: 'Datetime',
      labels: {
        formatter: (_, timestamp) => {
          return moment(timestamp).format(graphTimeFormat(ticks, min, max))
        },
        style: {
          colors: theme.palette.text.secondary
        }
      },
      tickAmount: ticks,
      tickPlacement: 'on',
      axisBorder: {
        color: theme.palette.divider
      },
      axisTicks: {
        show: true,
        color: theme.palette.divider
      }
    };
  }

  const buildChart = (series) => {
    return {
      series: series,
      type: 'area',
      options: {
        noData: {
          text: "No data available"
        },
        chart: {
          background: theme.palette.background.paper,
          toolbar: {
            show: false
          },
          animations: {
            enabled: false
          },
          zoom: {
            enabled: false
          }
        },
        dataLabels: {
          enabled: false
        },
        grid: {
          xaxis: {
            lines: {
              show: true
            }
          },
          yaxis: {
            lines: {
              show: true
            }
          },
          borderColor: theme.palette.divider
        },
        legend: {
          show: true,
          showForSingleSeries: true,
          position: 'bottom',
          horizontalAlign: 'right',
          labels: {
            colors: theme.palette.text.secondary
          }
        },
        markers: {
          size: 0
        },
        stroke: {
          width: 1,
          curve: 'straight',
          lineCap: 'butt'
        },
        title: {
          text: "Request and Error Rates (per second)",
          align: "center"
        },
        theme: {
          mode: theme.palette.type
        },
        tooltip: {
          theme: theme.palette.type,
          x: {
            formatter: (value) => (moment(value).format('dd/MM/yy HH:mm'))
          }
        },
        xaxis: [],
        yaxis: {
          decimalsInFloat: 2,
          axisTicks: {
            show: true,
            color: theme.palette.divider
          },
          axisBorder: {
            show: true,
            color: theme.palette.divider
          },
          labels: {
            style: {
              colors: theme.palette.text.secondary
            }
          }
        }
      }
    };
  }

  return (
    <Card>
      <CardContent >
        {chart && <Chart
          type="line"
          height="300"
          {...chart}
        />}
      </CardContent>
    </Card >
  );
};

RequestAndErrorRates.prototype = {
  className: PropTypes.string,
  functionId: PropTypes.string.isRequired
}

export default RequestAndErrorRates;